package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"time"
)

const (
	grantDeviceCode   = "urn:ietf:params:oauth:grant-type:device_code"
	grantRefreshToken = "refresh_token"

	errAuthorizationPending   = "authorization_pending"
	errSlowDown               = "slow_down"
	errExpiredToken           = "expired_token"
	errAccessDenied           = "access_denied"
	errInvalidGrant           = "invalid_grant"
	errInvalidRequest         = "invalid_request"
	errUnsupportedGrantType   = "unsupported_grant_type"
	errTemporarilyUnavailable = "temporarily_unavailable"
)

// handleToken serves the device_code and refresh_token grants.
func (g *Gateway) handleToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBodyBytes)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, errInvalidRequest, "malformed form body")
		return
	}
	switch r.PostFormValue("grant_type") {
	case grantDeviceCode:
		g.deviceCodeGrant(w, r)
	case grantRefreshToken:
		g.refreshTokenGrant(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, errUnsupportedGrantType, "")
	}
}

func (g *Gateway) deviceCodeGrant(w http.ResponseWriter, r *http.Request) {
	deviceCode := r.PostFormValue("device_code")
	if deviceCode == "" {
		writeOAuthError(w, http.StatusBadRequest, errInvalidRequest, "device_code is required")
		return
	}
	gr, outcome, err := g.grants.poll(r.Context(), deviceCode)
	if err != nil {
		g.logger.LogAttrs(r.Context(), slog.LevelError, "device grant store failed", slog.String("error", err.Error()))
		writeOAuthError(w, http.StatusServiceUnavailable, errTemporarilyUnavailable, "login is temporarily unavailable")
		return
	}
	switch outcome {
	case pollPending:
		writeOAuthError(w, http.StatusBadRequest, errAuthorizationPending, "")
		return
	case pollSlowDown:
		writeOAuthError(w, http.StatusBadRequest, errSlowDown, "")
		return
	case pollExpired:
		writeOAuthError(w, http.StatusBadRequest, errExpiredToken, "")
		return
	case pollDenied:
		writeOAuthError(w, http.StatusBadRequest, errAccessDenied, "")
		return
	case pollConsumed:
		g.fail(r, "device_code_reused")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "device code already used")
		return
	}
	// The client was checked at the callback; check again in case it was
	// removed while the device was polling.
	if c, ok := g.clients.Active().LookupEmail(gr.Email); !ok || !c.IsEnabled() {
		g.fail(r, "unknown_email")
		writeOAuthError(w, http.StatusBadRequest, errAccessDenied, "")
		return
	}
	g.issue(w, gr.Sub, gr.Email, gr.EncRefresh)
}

func (g *Gateway) refreshTokenGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	blob, err := g.refresh.open(r.PostFormValue("refresh_token"))
	if err != nil {
		g.fail(r, "refresh_invalid")
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "")
		return
	}
	if g.now().After(time.Unix(blob.AuthTime, 0).Add(g.cfg.SessionMax)) {
		g.fail(r, "session_max", slog.String("email", blob.Email))
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "session expired, run /login again")
		return
	}
	id, rotated, err := g.provider.Refresh(ctx, blob.IDPRefreshToken)
	if errors.Is(err, errIdPUnavailable) {
		g.logger.LogAttrs(ctx, slog.LevelError, "identity provider unavailable", slog.String("error", err.Error()))
		writeOAuthError(w, http.StatusServiceUnavailable, errTemporarilyUnavailable, "identity provider unavailable")
		return
	}
	if err != nil {
		g.fail(r, "refresh_rejected", slog.String("email", blob.Email), slog.String("error", err.Error()))
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "")
		return
	}
	if id.Subject != blob.Sub {
		g.fail(r, "refresh_sub_mismatch", slog.String("email", blob.Email))
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "")
		return
	}
	if id.Email == "" {
		// Some IdPs return no identity claims on refresh; the subject
		// matched, so the email from login still applies.
		id.Email, id.EmailVerified = blob.Email, true
	}
	if reason, _ := checkIdentity(id, g.cfg.AllowedDomain); reason != "" {
		g.fail(r, reason, slog.String("email", id.Email))
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "")
		return
	}
	if c, ok := g.clients.Active().LookupEmail(id.Email); !ok || !c.IsEnabled() {
		g.fail(r, "unknown_email", slog.String("email", id.Email))
		writeOAuthError(w, http.StatusBadRequest, errInvalidGrant, "")
		return
	}

	next := blob
	next.Email = id.Email
	if rotated != "" {
		next.IDPRefreshToken = rotated
	}
	g.issue(w, id.Subject, id.Email, g.refresh.seal(next))
}

// issue writes a token response with a new session JWT and, when one is
// held, the sealed refresh token.
func (g *Gateway) issue(w http.ResponseWriter, sub, email, refresh string) {
	resp := map[string]any{
		"access_token": g.sessions.Issue(sub, email),
		"token_type":   "Bearer",
		"expires_in":   int(g.sessions.TTL().Seconds()),
	}
	if refresh != "" {
		resp["refresh_token"] = refresh
	}
	writeJSON(w, http.StatusOK, resp)
}

// writeOAuthError renders an RFC 6749 section 5.2 error response.
func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	body := map[string]string{"error": code}
	if description != "" {
		body["error_description"] = description
	}
	writeJSON(w, status, body)
}
