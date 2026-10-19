// Package gateway lets Claude Code sign in to proem with company SSO through
// /login. Proem acts as the Claude Code enterprise gateway: it runs the OAuth
// 2.0 device authorization grant (RFC 8628) towards Claude Code, signs the
// user in with an OpenID Connect provider in the browser, and issues its own
// short-lived session JWT. The proxy's Auth middleware maps the JWT's email
// to a client in clients.yaml on every request.
package gateway

import (
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/metrics"
	"github.com/barockok/proem/internal/proxy"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

// Defaults for the optional Config fields.
const (
	DefaultPollInterval = 5 * time.Second
	DefaultGrantTTL     = 10 * time.Minute
	DefaultSessionTTL   = time.Hour
	DefaultSessionMax   = 30 * 24 * time.Hour
)

// Config configures the gateway.
type Config struct {
	// PublicURL is the origin Claude Code and browsers reach proem at, for
	// example https://proem.example.com. It is the JWT issuer and the base
	// of every advertised endpoint.
	PublicURL string
	// AllowedDomain, when set, restricts logins to emails in this domain.
	AllowedDomain string

	JWTSecrets  [][]byte
	RefreshKeys [][]byte

	SessionTTL   time.Duration
	SessionMax   time.Duration
	GrantTTL     time.Duration
	PollInterval time.Duration

	// RateLimit requests per RateWindow are allowed per IP on /device.
	RateLimit  int
	RateWindow time.Duration
}

// registryLoader is the subset of client.Loader the gateway needs.
type registryLoader interface{ Active() *client.Registry }

// Gateway serves the device flow, the IdP callback and the token endpoint.
type Gateway struct {
	cfg      Config
	public   *url.URL
	clients  registryLoader
	sessions *Sessions
	refresh  *RefreshCodec
	grants   *grantStore
	provider Provider
	limiter  *rateLimiter
	logger   *slog.Logger
	now      func() time.Time
}

// New builds a Gateway. rdb holds device grants; provider is the IdP.
func New(cfg Config, provider Provider, rdb redis.UniversalClient, clients registryLoader, logger *slog.Logger) (*Gateway, error) {
	public, err := url.Parse(strings.TrimRight(cfg.PublicURL, "/"))
	if err != nil || public.Scheme == "" || public.Host == "" {
		return nil, errors.New("gateway: public URL must be an absolute URL")
	}
	if provider == nil || rdb == nil || clients == nil {
		return nil, errors.New("gateway: provider, redis and clients are required")
	}
	cfg.PublicURL = public.String()
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.SessionMax <= 0 {
		cfg.SessionMax = DefaultSessionMax
	}
	if cfg.GrantTTL <= 0 {
		cfg.GrantTTL = DefaultGrantTTL
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.RateLimit <= 0 || cfg.RateWindow <= 0 {
		cfg.RateLimit, cfg.RateWindow = 10, time.Minute
	}
	sessions, err := NewSessions(cfg.PublicURL, cfg.SessionTTL, cfg.JWTSecrets)
	if err != nil {
		return nil, err
	}
	codec, err := NewRefreshCodec(cfg.RefreshKeys)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	g := &Gateway{
		cfg:      cfg,
		public:   public,
		clients:  clients,
		sessions: sessions,
		refresh:  codec,
		provider: provider,
		logger:   logger,
		now:      time.Now,
	}
	g.grants = &grantStore{rdb: rdb, ttl: cfg.GrantTTL, interval: cfg.PollInterval, now: g.clock, userCode: randomUserCode}
	g.limiter = newRateLimiter(cfg.RateLimit, cfg.RateWindow, g.clock)
	sessions.now = g.clock
	return g, nil
}

// clock indirects through g.now so tests can move time for every part.
func (g *Gateway) clock() time.Time { return g.now() }

// Sessions returns the session verifier for the proxy's Auth middleware.
func (g *Gateway) Sessions() *Sessions { return g.sessions }

// RedirectURL is the callback URL to register at the IdP.
func RedirectURL(publicURL string) string {
	return strings.TrimRight(publicURL, "/") + callbackPath
}

const (
	metadataPath      = "/.well-known/oauth-authorization-server"
	deviceAuthPath    = "/oauth/device_authorization"
	tokenPath         = "/oauth/token"
	devicePath        = "/device"
	callbackPath      = "/oauth/callback"
	managedPath       = "/managed/settings"
	deviceCookie      = "gw_dev"
	csrfCookie        = "gw_csrf"
	maxFormBodyBytes  = 64 << 10
	maxTelemetryBytes = 16 << 20
)

// Routes mounts the gateway's endpoints on r.
func (g *Gateway) Routes(r chi.Router) {
	r.Get(metadataPath, g.handleMetadata)
	r.Post(deviceAuthPath, g.handleDeviceAuthorization)
	r.Get(devicePath, g.handleDevicePage)
	r.Post(devicePath, g.handleDeviceSubmit)
	r.Get(callbackPath, g.handleCallback)
	r.Post(tokenPath, g.handleToken)
	r.Get(managedPath, g.handleManagedSettings)
	for _, signal := range []string{"metrics", "logs", "traces"} {
		r.Post("/v1/"+signal, g.handleTelemetry)
	}
}

func (g *Gateway) endpoint(path string) string { return g.cfg.PublicURL + path }

// secureCookies is false only for a plain-http loopback public URL.
func (g *Gateway) secureCookies() bool { return g.public.Scheme == "https" }

// fail counts and logs a rejection. Tokens are never logged.
func (g *Gateway) fail(r *http.Request, reason string, attrs ...slog.Attr) {
	metrics.AuthFailures.WithLabelValues(reason).Inc()
	g.logger.LogAttrs(r.Context(), slog.LevelWarn, "gateway auth failed",
		append([]slog.Attr{
			slog.String("reason", reason),
			slog.String("ip", proxy.ClientIP(r)),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		}, attrs...)...)
}

// pageData is rendered by page.
type pageData struct {
	Title       string
	Message     string
	Code        string // a pre-selected user code
	CSRF        string
	Started     string
	RequesterIP string
	Confirm     bool
}

var pages = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} - proem</title>
<style>
body{font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem;color:#1d1d1f;background:#fff}
.warn{border-left:4px solid #b54708;background:#fffaeb;padding:.75rem 1rem}
dl{display:grid;grid-template-columns:auto 1fr;gap:.25rem 1rem}
input[name=user_code]{font:1.5rem monospace;letter-spacing:.2em;text-transform:uppercase;width:12ch}
button{font-size:1rem;padding:.5rem 1.25rem;margin-top:1rem}
</style></head><body>
<h1>{{.Title}}</h1>
{{if .Confirm}}
<p class="warn"><strong>Only continue if you just ran <code>/login</code> in Claude Code on your own computer.</strong>
If someone sent you this link, close this page.</p>
{{if .Started}}<dl><dt>Request started</dt><dd>{{.Started}}</dd><dt>From IP address</dt><dd>{{.RequesterIP}}</dd></dl>{{end}}
<form method="post" action="/device">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<input type="hidden" name="selected" value="{{.Code}}">
<p><label for="user_code">Type the code shown in your terminal:</label></p>
<input id="user_code" name="user_code" autocomplete="off" autofocus required maxlength="9" placeholder="XXXX-XXXX">
<br><button type="submit">Continue to sign in</button>
</form>
{{else}}<p>{{.Message}}</p>{{end}}
</body></html>`))

// page renders an HTML page. Every page forbids framing so the confirm form
// cannot be clickjacked.
func page(w http.ResponseWriter, status int, d pageData) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https: http:; frame-ancestors 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	_ = pages.Execute(w, d)
}

func messagePage(w http.ResponseWriter, status int, title, message string) {
	page(w, status, pageData{Title: title, Message: message})
}
