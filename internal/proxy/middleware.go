package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/barockok/proem/internal/clientip"
)

type scopeCtxKey int

const requestScopeCtxKey scopeCtxKey = iota

// scope carries per-request facts that middleware needs to share. A context
// value only flows downwards, so inner middleware records what it learns here
// for outer middleware (the access log) to read once the request completes.
type scope struct {
	ip     string
	client string
}

func scopeFrom(ctx context.Context) *scope {
	s, _ := ctx.Value(requestScopeCtxKey).(*scope)
	return s
}

// ClientIP returns the caller's address as resolved by RealIP, or "" when the
// request did not pass through it.
func ClientIP(r *http.Request) string {
	if s := scopeFrom(r.Context()); s != nil {
		return s.ip
	}
	return ""
}

// RealIP resolves the caller's address once, per the trusted-proxy policy, and
// opens the request scope used by the rest of the chain.
func RealIP(resolver *clientip.Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), requestScopeCtxKey, &scope{ip: resolver.FromRequest(r)})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AccessLog records one line per request after it completes.
//
// Request and response bodies are never logged: they carry prompts, completions
// and credentials. The URL query is omitted for the same reason — only the path
// is recorded. Health checks are skipped to keep probe traffic out of the log.
func AccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.Int("bytes", rec.written),
			slog.String("client", loggedClient(r)),
			slog.String("ip", ClientIP(r)),
			slog.String("user_agent", r.UserAgent()),
		)
	})
}

// loggedClient reports the authenticated client. Auth runs inside this
// middleware, so the name is read back from the request scope rather than from
// the context, which by then belongs to a request we no longer hold.
func loggedClient(r *http.Request) string {
	if s := scopeFrom(r.Context()); s != nil && s.client != "" {
		return s.client
	}
	return ClientName(r.Context())
}

// statusRecorder captures the response status and size for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
	wrote   bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true
	n, err := s.ResponseWriter.Write(b)
	s.written += n
	return n, err
}

// Flush keeps streaming responses working through the wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
