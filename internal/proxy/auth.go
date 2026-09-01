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

// Auth authenticates callers against the client registry. The presented token
// is stripped from the request so it never reaches an upstream: the pool
// member's own credential is injected downstream instead.
//
// Rejections are counted and logged so a leaked or probed token is visible.
// The token itself is never logged; a short digest fingerprint is recorded
// instead, which is enough to correlate repeated attempts.
func Auth(loader registryLoader, logger *slog.Logger, next http.Handler) http.Handler {
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

		c, ok := loader.Active().Lookup(token)
		if !ok {
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
