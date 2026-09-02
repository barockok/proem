package proxy

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/barockok/proem/internal/clientip"
	"github.com/barockok/proem/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// UnknownForTest is the client label used when a request never passed Auth.
const UnknownForTest = "unknown"

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// logLine parses the first JSON log record written to buf.
func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line, _, _ := strings.Cut(strings.TrimSpace(buf.String()), "\n")
	if line == "" {
		t.Fatal("nothing was logged")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, line)
	}
	return m
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello there"))
	})
}

func TestAccessLogRecordsRequestShape(t *testing.T) {
	var buf bytes.Buffer
	h := AccessLog(jsonLogger(&buf), okHandler())

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("User-Agent", "claude-cli/2.1.251")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(withClient(req.Context(), "agent-maria")))

	entry := logLine(t, &buf)
	if entry["msg"] != "request" {
		t.Fatalf("msg %v", entry["msg"])
	}
	if entry["method"] != "POST" || entry["path"] != "/v1/messages" {
		t.Fatalf("method/path: %v %v", entry["method"], entry["path"])
	}
	if entry["status"] != float64(http.StatusCreated) {
		t.Fatalf("status %v", entry["status"])
	}
	if entry["bytes"] != float64(len("hello there")) {
		t.Fatalf("bytes %v", entry["bytes"])
	}
	if entry["client"] != "agent-maria" {
		t.Fatalf("client %v", entry["client"])
	}
	if entry["user_agent"] != "claude-cli/2.1.251" {
		t.Fatalf("user_agent %v", entry["user_agent"])
	}
	if _, ok := entry["duration_ms"]; !ok {
		t.Fatal("duration_ms missing")
	}
}

// Bodies carry prompts, completions and credentials. They must never be logged.
func TestAccessLogNeverRecordsBodies(t *testing.T) {
	var buf bytes.Buffer
	const secretPrompt = "SUPER-SECRET-PROMPT-TEXT"
	const secretReply = "SUPER-SECRET-COMPLETION"

	h := AccessLog(jsonLogger(&buf), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(secretReply))
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(secretPrompt))
	h.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), secretPrompt) {
		t.Fatal("request body leaked into the access log")
	}
	if strings.Contains(buf.String(), secretReply) {
		t.Fatal("response body leaked into the access log")
	}
}

// The query string can carry caller-supplied values; only the path is logged.
func TestAccessLogOmitsQueryString(t *testing.T) {
	var buf bytes.Buffer
	h := AccessLog(jsonLogger(&buf), okHandler())
	req := httptest.NewRequest(http.MethodGet, "/v1/messages?secret=leaky-value", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), "leaky-value") {
		t.Fatal("query string leaked into the access log")
	}
	if logLine(t, &buf)["path"] != "/v1/messages" {
		t.Fatalf("path should be bare, got %v", logLine(t, &buf)["path"])
	}
}

func TestAccessLogSkipsHealthChecks(t *testing.T) {
	var buf bytes.Buffer
	h := AccessLog(jsonLogger(&buf), okHandler())
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/health", nil))
	if buf.Len() != 0 {
		t.Fatalf("health probes should not be logged, got %s", buf.String())
	}
}

func TestAccessLogDefaultsToStatus200(t *testing.T) {
	var buf bytes.Buffer
	h := AccessLog(jsonLogger(&buf), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("implicit 200"))
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if logLine(t, &buf)["status"] != float64(200) {
		t.Fatalf("status %v", logLine(t, &buf)["status"])
	}
}

func TestStatusRecorderKeepsFirstStatusAndFlushes(t *testing.T) {
	rec := httptest.NewRecorder()
	s := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}
	s.WriteHeader(http.StatusTeapot)
	s.WriteHeader(http.StatusInternalServerError) // ignored, as net/http would
	if s.status != http.StatusTeapot {
		t.Fatalf("status %d", s.status)
	}
	s.Flush() // must not panic when the writer supports flushing
	if !rec.Flushed {
		t.Fatal("Flush did not reach the underlying writer")
	}
}

func TestRealIPPutsAddressOnContext(t *testing.T) {
	resolver, err := clientip.NewResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	h := RealIP(resolver, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ClientIP(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.RemoteAddr = "10.1.1.1:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "203.0.113.9" {
		t.Fatalf("resolved ip %q", seen)
	}
}

func TestClientIPEmptyWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := ClientIP(req); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestAccessLogRecordsResolvedIP(t *testing.T) {
	var buf bytes.Buffer
	resolver, _ := clientip.NewResolver(nil)
	h := RealIP(resolver, AccessLog(jsonLogger(&buf), okHandler()))

	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.RemoteAddr = "198.51.100.7:9999"
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := logLine(t, &buf)["ip"]; got != "198.51.100.7" {
		t.Fatalf("ip %v", got)
	}
}

func TestAuthFailuresAreCountedAndLogged(t *testing.T) {
	cases := []struct {
		name   string
		header string
		value  string
		reason string
		status int
		wantFP bool
	}{
		{name: "missing", reason: "missing_credentials", status: http.StatusUnauthorized},
		{name: "unknown", header: "Authorization", value: "Bearer sk-ant-oat01-nope",
			reason: "unknown_token", status: http.StatusUnauthorized, wantFP: true},
		{name: "disabled", header: "Authorization", value: "Bearer " + soraToken,
			reason: "client_disabled", status: http.StatusForbidden},
	}

	for _, tc := range cases {
		var buf bytes.Buffer
		before := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues(tc.reason))

		h := Auth(testRegistry(t), jsonLogger(&buf), echoClient(new(string)))
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.RemoteAddr = "203.0.113.5:1234"
		if tc.header != "" {
			req.Header.Set(tc.header, tc.value)
		}
		resolver, _ := clientip.NewResolver(nil)
		rec := httptest.NewRecorder()
		RealIP(resolver, h).ServeHTTP(rec, req)

		if rec.Code != tc.status {
			t.Fatalf("%s: status %d", tc.name, rec.Code)
		}
		after := testutil.ToFloat64(metrics.AuthFailures.WithLabelValues(tc.reason))
		if after-before != 1 {
			t.Fatalf("%s: counter delta %v", tc.name, after-before)
		}

		entry := logLine(t, &buf)
		if entry["msg"] != "auth failed" || entry["reason"] != tc.reason {
			t.Fatalf("%s: log %v", tc.name, entry)
		}
		if entry["ip"] != "203.0.113.5" {
			t.Fatalf("%s: ip %v", tc.name, entry["ip"])
		}
		if entry["level"] != "WARN" {
			t.Fatalf("%s: level %v", tc.name, entry["level"])
		}
		if _, ok := entry["token_fp"]; ok != tc.wantFP {
			t.Fatalf("%s: token_fp present=%v want=%v", tc.name, ok, tc.wantFP)
		}
	}
}

// A rejected credential must never appear in the log, only its fingerprint.
func TestRejectedTokenIsNeverLogged(t *testing.T) {
	var buf bytes.Buffer
	const attempted = "sk-ant-oat01-this-must-not-appear"

	h := Auth(testRegistry(t), jsonLogger(&buf), echoClient(new(string)))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+attempted)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if strings.Contains(buf.String(), attempted) {
		t.Fatalf("the attempted token leaked into the log: %s", buf.String())
	}
	fp := tokenFingerprint(attempted)
	if !strings.Contains(buf.String(), fp) {
		t.Fatal("fingerprint missing, so repeated attempts cannot be correlated")
	}
	if len(fp) != 12 {
		t.Fatalf("fingerprint length %d", len(fp))
	}
}

func TestFingerprintIsStableAndDistinct(t *testing.T) {
	if tokenFingerprint("a") != tokenFingerprint("a") {
		t.Fatal("fingerprint not stable")
	}
	if tokenFingerprint("a") == tokenFingerprint("b") {
		t.Fatal("fingerprints collided")
	}
}

// Regression: Auth runs inside AccessLog, and a context value set there cannot
// travel back out. The client name must still reach the log line.
func TestAccessLogRecordsAuthenticatedClient(t *testing.T) {
	var buf bytes.Buffer
	resolver, _ := clientip.NewResolver(nil)
	chain := RealIP(resolver, AccessLog(jsonLogger(&buf),
		Auth(testRegistry(t), testLogger(nil), okHandler())))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+mariaToken)
	chain.ServeHTTP(httptest.NewRecorder(), req)

	if got := logLine(t, &buf)["client"]; got != "agent-maria" {
		t.Fatalf("access log must name the authenticated client, got %v", got)
	}
}

func TestAccessLogLabelsRejectedRequestsUnknown(t *testing.T) {
	var buf bytes.Buffer
	resolver, _ := clientip.NewResolver(nil)
	chain := RealIP(resolver, AccessLog(jsonLogger(&buf),
		Auth(testRegistry(t), testLogger(nil), okHandler())))

	chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	entry := logLine(t, &buf)
	if entry["client"] != "unknown" {
		t.Fatalf("client %v", entry["client"])
	}
	if entry["status"] != float64(http.StatusUnauthorized) {
		t.Fatalf("status %v", entry["status"])
	}
}
