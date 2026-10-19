package gateway

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/barockok/proem/internal/proxy"
)

// handleDeviceAuthorization starts a device flow (RFC 8628 section 3.1).
// Claude Code sends an empty form body; no client authentication is used.
func (g *Gateway) handleDeviceAuthorization(w http.ResponseWriter, r *http.Request) {
	deviceCode, userCode, err := g.grants.create(r.Context(), proxy.ClientIP(r))
	if err != nil {
		g.logger.LogAttrs(r.Context(), slog.LevelError, "device authorization failed", slog.String("error", err.Error()))
		writeOAuthError(w, http.StatusServiceUnavailable, errTemporarilyUnavailable, "login is temporarily unavailable")
		return
	}
	verify := g.endpoint(devicePath)
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 formatUserCode(userCode),
		"verification_uri":          verify,
		"verification_uri_complete": verify + "?user_code=" + url.QueryEscape(formatUserCode(userCode)),
		"expires_in":                int(g.cfg.GrantTTL.Seconds()),
		"interval":                  int(g.cfg.PollInterval.Seconds()),
	})
}

// handleDevicePage shows the confirm page. A user_code in the URL only
// pre-selects the grant: the user must still type the code from their
// terminal, which defeats a phishing link that would otherwise approve an
// attacker's grant in one click (RFC 8628 section 5.4).
func (g *Gateway) handleDevicePage(w http.ResponseWriter, r *http.Request) {
	if !g.allowDevice(w, r) {
		return
	}
	d := pageData{Title: "Sign in to proem", Confirm: true}
	if raw := r.URL.Query().Get("user_code"); raw != "" {
		code := normalizeUserCode(raw)
		gr, _, err := g.grants.byUserCode(r.Context(), code)
		if err != nil {
			g.deviceLookupFailed(w, r, err)
			return
		}
		d.Code = formatUserCode(code)
		d.Started = time.Unix(gr.Created, 0).UTC().Format("2006-01-02 15:04:05 MST")
		d.RequesterIP = gr.RequesterIP
	}
	csrf := randomToken(32)
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: devicePath, MaxAge: int(g.cfg.GrantTTL.Seconds()),
		HttpOnly: true, Secure: g.secureCookies(), SameSite: http.SameSiteStrictMode,
	})
	d.CSRF = csrf
	page(w, http.StatusOK, d)
}

// handleDeviceSubmit checks the typed code and sends the browser to the IdP.
func (g *Gateway) handleDeviceSubmit(w http.ResponseWriter, r *http.Request) {
	if !g.allowDevice(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		messagePage(w, http.StatusBadRequest, "Bad request", "The form could not be read.")
		return
	}
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.PostFormValue("csrf"))) != 1 {
		g.fail(r, "device_csrf")
		messagePage(w, http.StatusForbidden, "Session expired",
			"This page expired. Open the link from Claude Code again.")
		return
	}

	typed := normalizeUserCode(r.PostFormValue("user_code"))
	selected := normalizeUserCode(r.PostFormValue("selected"))
	if selected == "" {
		selected = typed
	}
	if typed == "" || subtle.ConstantTimeCompare([]byte(typed), []byte(selected)) != 1 {
		g.fail(r, "device_code_mismatch")
		messagePage(w, http.StatusBadRequest, "Code does not match",
			"The code you typed does not match this sign-in request. Check the code in your terminal, or close this page if you did not start this sign-in.")
		return
	}
	gr, dcHash, err := g.grants.byUserCode(r.Context(), selected)
	if err != nil {
		g.deviceLookupFailed(w, r, err)
		return
	}

	state, nonce, verifier := randomToken(32), randomToken(32), randomToken(32)
	authURL, err := g.provider.AuthCodeURL(r.Context(), state, nonce, verifier)
	if err != nil {
		g.logger.LogAttrs(r.Context(), slog.LevelError, "identity provider unavailable", slog.String("error", err.Error()))
		messagePage(w, http.StatusServiceUnavailable, "Sign-in unavailable",
			"The identity provider cannot be reached. Try again in a few minutes.")
		return
	}
	if err := g.grants.startLogin(r.Context(), dcHash, gr, state, nonce, verifier); err != nil {
		g.deviceLookupFailed(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: deviceCookie, Value: dcHash, Path: callbackPath, MaxAge: int(g.cfg.GrantTTL.Seconds()),
		// Lax, not Strict: the IdP's redirect back to the callback is a
		// cross-site navigation, and Strict would drop the cookie.
		HttpOnly: true, Secure: g.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, authURL, http.StatusFound)
}

// deviceLookupFailed renders the page for an unknown, used or expired code,
// or for a Redis failure.
func (g *Gateway) deviceLookupFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errGrantNotFound) || errors.Is(err, errGrantState) {
		g.fail(r, "device_code_unknown")
		messagePage(w, http.StatusNotFound, "Code not valid",
			"This code is unknown, already used or expired. Run /login in Claude Code again.")
		return
	}
	g.logger.LogAttrs(r.Context(), slog.LevelError, "device grant store failed", slog.String("error", err.Error()))
	messagePage(w, http.StatusServiceUnavailable, "Sign-in unavailable", "Try again in a few minutes.")
}

// allowDevice applies the per-IP rate limit on /device. User codes have
// about 34.6 bits of entropy, so guessing must be slow.
func (g *Gateway) allowDevice(w http.ResponseWriter, r *http.Request) bool {
	if g.limiter.allow(proxy.ClientIP(r)) {
		return true
	}
	g.fail(r, "device_rate_limited")
	w.Header().Set("Retry-After", "60")
	messagePage(w, http.StatusTooManyRequests, "Too many attempts", "Wait a minute and try again.")
	return false
}

// rateLimiter is a fixed-window counter per key, held in memory. Each proem
// replica limits on its own, which is enough to make guessing impractical.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	n     int
}

// maxTrackedKeys bounds the limiter's memory; expired windows are swept
// when it is reached.
const maxTrackedKeys = 10000

func newRateLimiter(limit int, window time.Duration, now func() time.Time) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, now: now, hits: map[string]*rateWindow{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		if !ok && len(l.hits) >= maxTrackedKeys {
			for k, v := range l.hits {
				if now.Sub(v.start) >= l.window {
					delete(l.hits, k)
				}
			}
		}
		l.hits[key] = &rateWindow{start: now, n: 1}
		return true
	}
	if w.n >= l.limit {
		return false
	}
	w.n++
	return true
}
