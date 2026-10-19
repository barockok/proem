package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/metrics"
)

type ctxKey int

const clientCtxKey ctxKey = iota

// ClientName returns the authenticated client for a request, or
// client.UnknownClient when the request did not pass through Auth.
func ClientName(ctx context.Context) string {
	if name, ok := ctx.Value(clientCtxKey).(string); ok && name != "" {
		return name
	}
	return client.UnknownClient
}

// withClient tags a context with the authenticated client name.
func withClient(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, clientCtxKey, name)
}

// registryLoader is the subset of client.Loader the middleware needs.
type registryLoader interface{ Active() *client.Registry }

// SessionVerifier verifies an SSO session token issued by the gateway and
// returns the email it was issued for.
type SessionVerifier interface {
	VerifySession(token string) (email string, err error)
}

// Auth authenticates callers against the client registry. The presented token
// is stripped from the request so it never reaches an upstream: the pool
// member's own credential is injected downstream instead.
//
// Rejections are counted and logged so a leaked or probed token is visible.
// The token itself is never logged; a short digest fingerprint is recorded
// instead, which is enough to correlate repeated attempts.
func Auth(loader registryLoader, logger *slog.Logger, next http.Handler) http.Handler {
	return AuthWithSessions(loader, nil, logger, next)
}

// AuthWithSessions is Auth that also accepts SSO session tokens. When
// sessions is set, a token shaped like a JWT must verify as a session and
// resolves to a client by email; it never falls through to the hash lookup.
// Every other token takes the hash lookup unchanged.
func AuthWithSessions(loader registryLoader, sessions SessionVerifier, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reject := func(status int, reason, errType, message string, attrs ...slog.Attr) {
			metrics.AuthFailures.WithLabelValues(reason).Inc()
			logger.LogAttrs(r.Context(), slog.LevelWarn, "auth failed",
				append([]slog.Attr{
					slog.String("reason", reason),
					slog.String("ip", ClientIP(r)),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("user_agent", r.UserAgent()),
				}, attrs...)...)
			writeAuthError(w, status, errType, message)
		}

		token := presentedToken(r)
		if token == "" {
			reject(http.StatusUnauthorized, "missing_credentials", "authentication_error",
				"missing credentials: set CLAUDE_CODE_OAUTH_TOKEN to the token issued for this agent")
			return
		}

		var c client.Client
		var ok bool
		if sessions != nil && looksLikeJWT(token) {
			email, err := sessions.VerifySession(token)
			if err != nil {
				reject(http.StatusUnauthorized, "invalid_session", "authentication_error",
					"invalid or expired session: run /login in Claude Code",
					slog.String("token_fp", tokenFingerprint(token)))
				return
			}
			if c, ok = loader.Active().LookupEmail(email); !ok {
				reject(http.StatusUnauthorized, "unknown_email", "authentication_error",
					"this account is not registered with the proxy",
					slog.String("email", email))
				return
			}
		} else if c, ok = loader.Active().Lookup(token); !ok {
			reject(http.StatusUnauthorized, "unknown_token", "authentication_error",
				"invalid credentials: this token is not registered with the proxy",
				slog.String("token_fp", tokenFingerprint(token)))
			return
		}
		if !c.IsEnabled() {
			reject(http.StatusForbidden, "client_disabled", "permission_error",
				"client "+c.Name+" is disabled",
				slog.String("client", c.Name))
			return
		}

		stripClientCredentials(r)
		if s := scopeFrom(r.Context()); s != nil {
			s.client = c.Name // so the access log, which wraps this, can see it
		}
		next.ServeHTTP(w, r.WithContext(withClient(r.Context(), c.Name)))
	})
}

// looksLikeJWT reports whether token is three dot-separated base64url parts.
// Issued sk-ant-oat01- tokens never contain a dot.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, p := range parts {
		for _, r := range p {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return false
			}
		}
	}
	return true
}

// tokenFingerprint returns a short, non-reversible handle for a rejected
// token so repeated attempts can be correlated in logs without recording the
// credential itself.
func tokenFingerprint(token string) string {
	return client.HashToken(token)[:12]
}

// presentedToken pulls the caller's credential from either header the Anthropic
// SDKs use: OAuth tokens arrive as a bearer, API keys as x-api-key.
func presentedToken(r *http.Request) string {
	if v := r.Header.Get("Authorization"); v != "" {
		if rest, ok := cutBearer(v); ok {
			return rest
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}

func cutBearer(v string) (string, bool) {
	const prefix = "bearer "
	trimmed := strings.TrimSpace(v)
	if len(trimmed) < len(prefix) || !strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(trimmed[len(prefix):]), true
}

// stripClientCredentials removes the caller's own auth headers. Without this an
// x-api-key from the client would survive header copying and, because Anthropic
// gives the API key precedence over a bearer token, override the OAuth
// credential the proxy injects for the chosen pool member.
func stripClientCredentials(r *http.Request) {
	r.Header.Del("Authorization")
	r.Header.Del("x-api-key")
}

// writeAuthError renders an Anthropic-shaped error so SDK clients surface it
// as an authentication failure rather than an opaque proxy error.
func writeAuthError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
}
