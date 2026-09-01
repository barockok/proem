package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/config"
)

func writePool(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return fmt.Sprintf("127.0.0.1:%d", l.Addr().(*net.TCPAddr).Port)
}

// validPool points at an unroutable address on purpose: tests must never
// reach the real API, and a fast dial failure is enough to prove routing.
const validPool = `
members:
  - id: a
    type: anthropic_oauth
    cred: {env: TEST_APP_OAT}
    baseURL: https://127.0.0.1:1
`

const testToken = "sk-ant-oat01-test-token"

func writeClients(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clients.yaml")
	body := "clients:\n  - name: agent-test\n    tokenSHA256: " + client.HashToken(testToken) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func testConfig(t *testing.T, poolPath string) config.Config {
	cfg := config.DefaultConfig()
	cfg.ConfigPath = poolPath
	cfg.ClientsPath = writeClients(t)
	cfg.RedisURL = ""
	cfg.ListenAddr = freePort(t)
	cfg.MetricsAddr = freePort(t)
	return cfg
}

func TestNewRejectsBadPool(t *testing.T) {
	if _, err := New(testConfig(t, writePool(t, "members: []"))); err == nil {
		t.Fatal("want error for empty pool")
	}
	if _, err := New(testConfig(t, filepath.Join(t.TempDir(), "missing.yaml"))); err == nil {
		t.Fatal("want error for missing pool file")
	}
}

func TestNewWithRedis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()

	cfg := testConfig(t, writePool(t, validPool))
	cfg.RedisURL = "redis://" + mr.Addr()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.store == nil {
		t.Fatal("store should be connected")
	}
}

func TestNewBadRedisURLIsNonFatal(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	cfg.RedisURL = "://not-a-url"
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("bad redis URL should not fail startup: %v", err)
	}
	defer a.Close()
	if a.store != nil {
		t.Fatal("store should be nil when redis URL is invalid")
	}
}

func TestHealthEndpoint(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health status %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("health body %q", rec.Body.String())
	}
}

func TestProxyRouteReachesHandler(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// The pool member is unroutable, so the request fails upstream — but
	// reaching a proxy error (not 404, not 401) proves the request passed
	// authentication and landed on the failover handler.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusNotFound {
		t.Fatal("catch-all route not wired to proxy handler")
	}
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("valid token rejected")
	}
}

func TestMetricsHandler(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	rec := httptest.NewRecorder()
	a.MetricsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "proem_config_reloads_total") {
		t.Fatal("expected proem metrics in output")
	}
}

func TestRunServesAndShutsDown(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	// poll until the listener accepts
	client := &http.Client{Timeout: time.Second}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = client.Get("http://" + cfg.ListenAddr + "/health")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", resp.StatusCode)
	}

	// metrics listener is up too
	mResp, err := client.Get("http://" + cfg.MetricsAddr + "/metrics")
	if err != nil {
		t.Fatalf("metrics not serving: %v", err)
	}
	mResp.Body.Close()

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}

func TestRunReturnsListenError(t *testing.T) {
	// occupy the port so ListenAndServe fails immediately
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	cfg := testConfig(t, writePool(t, validPool))
	cfg.ListenAddr = busy.Addr().String()
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Run(ctx); err == nil {
		t.Fatal("want error when listen address is taken")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestNewRejectsBadClientsRegistry(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	cfg.ClientsPath = filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := New(cfg); err == nil {
		t.Fatal("missing clients.yaml must fail startup")
	}

	empty := filepath.Join(t.TempDir(), "clients.yaml")
	os.WriteFile(empty, []byte("clients: []"), 0o644)
	cfg2 := testConfig(t, writePool(t, validPool))
	cfg2.ClientsPath = empty
	if _, err := New(cfg2); err == nil {
		t.Fatal("empty clients.yaml must fail startup")
	}
}

func TestUnauthenticatedRequestIsRejected(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request should be 401, got %d", rec.Code)
	}
}

func TestHealthEndpointNeedsNoAuth(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health must stay unauthenticated for probes, got %d", rec.Code)
	}
}

func TestNewRejectsBadTrustedProxies(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	cfg.TrustedProxies = []string{"not-a-cidr"}
	if _, err := New(cfg); err == nil {
		t.Fatal("malformed trusted proxy entry must fail startup")
	}
}

func TestAccessLogCanBeDisabled(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	cfg.AccessLog = false
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// still serves normally with logging off
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestJSONLogFormatStarts(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	cfg.LogFormat = "json"
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Handler() == nil {
		t.Fatal("handler nil")
	}
}
