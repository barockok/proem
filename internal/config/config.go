package config

import (
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// StickyMode controls session affinity.
type StickyMode string

const (
	StickyLB    StickyMode = "lb"
	StickyRedis StickyMode = "redis"
	StickyNone  StickyMode = "none"
)

// Config holds proxy runtime options.
type Config struct {
	ConfigPath      string
	ClientsPath     string
	RedisURL        string
	ListenAddr      string
	MetricsAddr     string
	StickyMode      StickyMode
	AccessLog       bool
	LogFormat       string
	TrustedProxies  []string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	UpstreamTimeout time.Duration

	// TLSCert and TLSKey, when both set, serve the proxy listener with TLS.
	TLSCert string
	TLSKey  string

	// Gateway is the SSO gateway; it is off unless PublicURL is set.
	Gateway Gateway
}

// Gateway configures SSO login through Claude Code's /login.
type Gateway struct {
	PublicURL         string
	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCScopes        []string
	OIDCAuthParams    map[string]string
	OIDCAllowedDomain string
	JWTSecrets        [][]byte
	RefreshKeys       [][]byte
	SessionTTL        time.Duration
	SessionMax        time.Duration
	DeviceRateLimit   int
	DeviceRateWindow  time.Duration
	DeviceGrantTTL    time.Duration
}

// Enabled reports whether the SSO gateway is configured.
func (g Gateway) Enabled() bool { return g.PublicURL != "" }

// DefaultConfig returns sane defaults.
func DefaultConfig() Config {
	return Config{
		ConfigPath:      "./pool.yaml",
		ClientsPath:     "./clients.yaml",
		RedisURL:        "redis://localhost:6379/0",
		ListenAddr:      ":8080",
		MetricsAddr:     ":9090",
		StickyMode:      StickyLB,
		AccessLog:       true,
		LogFormat:       "text",
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    60 * time.Second,
		UpstreamTimeout: 60 * time.Second,
		Gateway: Gateway{
			OIDCScopes:       []string{"openid", "email", "profile"},
			SessionTTL:       time.Hour,
			SessionMax:       720 * time.Hour,
			DeviceRateLimit:  10,
			DeviceRateWindow: time.Minute,
			DeviceGrantTTL:   10 * time.Minute,
		},
	}
}

// ParseFlags parses os.Args into Config.
func ParseFlags() (Config, error) {
	return ParseArgs(os.Args[0], os.Args[1:])
}

// ParseArgs parses an explicit argument list into Config. Kept separate from
// ParseFlags so tests can drive it without touching the global flag set.
func ParseArgs(name string, args []string) (Config, error) {
	cfg := DefaultConfig()
	var sticky, trustedProxies string
	gw := &cfg.Gateway
	scopes := envOr("OIDC_SCOPES", strings.Join(gw.OIDCScopes, ","))
	authParams := os.Getenv("OIDC_AUTH_PARAMS")
	rateLimit := "10/min"
	sessionTTL, err := envDuration("PROEM_SESSION_TTL", gw.SessionTTL)
	if err != nil {
		return cfg, err
	}
	sessionMax, err := envDuration("PROEM_SESSION_MAX", gw.SessionMax)
	if err != nil {
		return cfg, err
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "path to pool.yaml")
	fs.StringVar(&cfg.ClientsPath, "clients", cfg.ClientsPath, "path to clients.yaml (client tokens; required)")
	fs.StringVar(&cfg.RedisURL, "redis-url", cfg.RedisURL, "redis URL (redis://...)")
	fs.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "proxy listen addr")
	fs.StringVar(&cfg.MetricsAddr, "metrics-addr", cfg.MetricsAddr, "metrics listen addr")
	fs.StringVar(&sticky, "sticky-mode", string(cfg.StickyMode), "sticky mode: lb|redis|none")
	fs.BoolVar(&cfg.AccessLog, "access-log", cfg.AccessLog, "log one line per request (never includes bodies)")
	fs.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "log output format: text|json")
	fs.StringVar(&trustedProxies, "trusted-proxies", "", "comma-separated CIDRs or IPs whose X-Forwarded-For is believed; empty means never trust it")
	fs.DurationVar(&cfg.ReadTimeout, "read-timeout", cfg.ReadTimeout, "read timeout")
	fs.DurationVar(&cfg.WriteTimeout, "write-timeout", cfg.WriteTimeout, "write timeout")
	fs.DurationVar(&cfg.UpstreamTimeout, "upstream-timeout", cfg.UpstreamTimeout, "upstream request timeout")
	fs.StringVar(&cfg.TLSCert, "tls-cert", "", "TLS certificate file for the proxy listener")
	fs.StringVar(&cfg.TLSKey, "tls-key", "", "TLS key file for the proxy listener")
	fs.StringVar(&gw.PublicURL, "public-url", os.Getenv("PROEM_PUBLIC_URL"), "public URL of the SSO gateway; empty disables it")
	fs.StringVar(&gw.OIDCIssuer, "oidc-issuer", os.Getenv("OIDC_ISSUER"), "OpenID Connect issuer URL")
	fs.StringVar(&scopes, "oidc-scopes", scopes, "comma-separated OIDC scopes")
	fs.StringVar(&authParams, "oidc-auth-params", authParams, "extra authorize URL parameters as k=v,k=v")
	fs.StringVar(&gw.OIDCAllowedDomain, "oidc-allowed-domain", os.Getenv("OIDC_ALLOWED_DOMAIN"), "only allow emails in this domain")
	fs.DurationVar(&gw.SessionTTL, "session-ttl", sessionTTL, "SSO session token lifetime")
	fs.DurationVar(&gw.SessionMax, "session-max", sessionMax, "maximum time from SSO login to the last allowed refresh")
	fs.StringVar(&rateLimit, "device-rate-limit", rateLimit, "per-IP limit on /device, as N/s, N/min or N/h")
	fs.DurationVar(&gw.DeviceGrantTTL, "device-grant-ttl", gw.DeviceGrantTTL, "device grant lifetime")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}

	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return cfg, fmt.Errorf("--tls-cert and --tls-key must be set together")
	}
	gw.OIDCScopes = splitList(scopes)
	if gw.OIDCAuthParams, err = parseParams(authParams); err != nil {
		return cfg, err
	}
	if gw.DeviceRateLimit, gw.DeviceRateWindow, err = parseRate(rateLimit); err != nil {
		return cfg, err
	}
	if gw.Enabled() {
		if err := loadGatewaySecrets(gw); err != nil {
			return cfg, err
		}
		if cfg.RedisURL == "" {
			return cfg, fmt.Errorf("the SSO gateway needs --redis-url for device grants")
		}
	}

	switch cfg.LogFormat {
	case "text", "json":
	default:
		return cfg, fmt.Errorf("invalid --log-format %q: want text|json", cfg.LogFormat)
	}

	for _, entry := range strings.Split(trustedProxies, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			cfg.TrustedProxies = append(cfg.TrustedProxies, entry)
		}
	}

	if cfg.ClientsPath == "" {
		return cfg, fmt.Errorf("--clients is required: the proxy will not start without a client token registry")
	}

	switch StickyMode(sticky) {
	case StickyLB, StickyRedis, StickyNone:
		cfg.StickyMode = StickyMode(sticky)
	default:
		return cfg, fmt.Errorf("invalid --sticky-mode %q: want lb|redis|none", sticky)
	}
	return cfg, nil
}

// loadGatewaySecrets reads the IdP client credentials and keys from the
// environment (they come from a secret store, never from flags) and checks
// the gateway settings.
func loadGatewaySecrets(gw *Gateway) error {
	u, err := url.Parse(gw.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid --public-url %q: want an absolute https:// URL", gw.PublicURL)
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return fmt.Errorf("--public-url must use https:// unless the host is loopback")
	}
	if gw.OIDCIssuer == "" {
		return fmt.Errorf("the SSO gateway needs --oidc-issuer")
	}
	gw.OIDCClientID = os.Getenv("OIDC_CLIENT_ID")
	gw.OIDCClientSecret = os.Getenv("OIDC_CLIENT_SECRET")
	if gw.OIDCClientID == "" || gw.OIDCClientSecret == "" {
		return fmt.Errorf("the SSO gateway needs OIDC_CLIENT_ID and OIDC_CLIENT_SECRET")
	}
	for _, s := range splitList(os.Getenv("PROEM_JWT_SECRET")) {
		if len(s) < 32 {
			return fmt.Errorf("each PROEM_JWT_SECRET entry must be at least 32 bytes")
		}
		gw.JWTSecrets = append(gw.JWTSecrets, []byte(s))
	}
	if len(gw.JWTSecrets) == 0 {
		return fmt.Errorf("the SSO gateway needs PROEM_JWT_SECRET")
	}
	for _, s := range splitList(os.Getenv("PROEM_REFRESH_KEY")) {
		k, err := decodeKey(s)
		if err != nil || len(k) != 32 {
			return fmt.Errorf("each PROEM_REFRESH_KEY entry must be 32 bytes, base64-encoded")
		}
		gw.RefreshKeys = append(gw.RefreshKeys, k)
	}
	if len(gw.RefreshKeys) == 0 {
		return fmt.Errorf("the SSO gateway needs PROEM_REFRESH_KEY")
	}
	if gw.SessionTTL <= 0 || gw.SessionMax < gw.SessionTTL {
		return fmt.Errorf("--session-ttl must be positive and no longer than --session-max")
	}
	if gw.DeviceGrantTTL <= 0 {
		return fmt.Errorf("--device-grant-ttl must be positive")
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if k, err := enc.DecodeString(s); err == nil {
			return k, nil
		}
	}
	return nil, fmt.Errorf("not base64")
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// parseParams parses k=v,k=v.
func parseParams(s string) (map[string]string, error) {
	var out map[string]string
	for _, kv := range splitList(s) {
		if out == nil {
			out = map[string]string{}
		}
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("invalid --oidc-auth-params entry %q: want k=v", kv)
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out, nil
}

// parseRate parses N/s, N/min or N/h.
func parseRate(s string) (int, time.Duration, error) {
	n, unit, ok := strings.Cut(s, "/")
	count, err := strconv.Atoi(strings.TrimSpace(n))
	if !ok || err != nil || count <= 0 {
		return 0, 0, fmt.Errorf("invalid --device-rate-limit %q: want N/s, N/min or N/h", s)
	}
	switch strings.TrimSpace(unit) {
	case "s", "sec":
		return count, time.Second, nil
	case "m", "min":
		return count, time.Minute, nil
	case "h", "hour":
		return count, time.Hour, nil
	}
	return 0, 0, fmt.Errorf("invalid --device-rate-limit %q: want N/s, N/min or N/h", s)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %v", key, v, err)
	}
	return d, nil
}
