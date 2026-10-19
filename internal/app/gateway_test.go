package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
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
	"github.com/barockok/proem/internal/gateway"
)

var gwSecret = []byte(strings.Repeat("g", 32))

func gatewayConfig(t *testing.T) config.Config {
	t.Helper()
	mr := miniredis.RunT(t)
	cfg := testConfig(t, writePool(t, validPool))
	path := filepath.Join(t.TempDir(), "clients.yaml")
	body := "clients:\n  - name: agent-test\n    tokenSHA256: " + client.HashToken(testToken) +
		"\n  - name: alice\n    email: alice@example.com\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.ClientsPath = path
	cfg.RedisURL = "redis://" + mr.Addr()
	cfg.Gateway.PublicURL = "https://proem.example.com"
	cfg.Gateway.OIDCIssuer = "https://idp.invalid"
	cfg.Gateway.JWTSecrets = [][]byte{gwSecret}
	cfg.Gateway.RefreshKeys = [][]byte{[]byte(strings.Repeat("r", 32))}
	return cfg
}

func serve(a *App, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGatewayRoutesAndSessionAuth(t *testing.T) {
	cfg := gatewayConfig(t)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if rec := serve(a, http.MethodGet, "/.well-known/oauth-authorization-server", ""); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "https://proem.example.com/oauth/token") {
		t.Fatalf("metadata: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(a, http.MethodPost, "/oauth/device_authorization", ""); rec.Code != http.StatusOK {
		t.Fatalf("device authorization: %d", rec.Code)
	}
	if rec := serve(a, http.MethodGet, "/managed/settings", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("managed settings: %d", rec.Code)
	}

	sessions, _ := gateway.NewSessions(cfg.Gateway.PublicURL, time.Hour, [][]byte{gwSecret})
	jwt := sessions.Issue("sub", "alice@example.com")
	// Auth passes; the unroutable upstream then fails.
	if rec := serve(a, http.MethodPost, "/v1/messages", jwt); rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("session JWT rejected: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(a, http.MethodPost, "/v1/messages", jwt+"x"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tampered JWT: %d", rec.Code)
	}
	if rec := serve(a, http.MethodPost, "/v1/metrics", jwt); rec.Code != http.StatusOK {
		t.Fatalf("telemetry stub: %d", rec.Code)
	}
	// Token clients are unchanged.
	if rec := serve(a, http.MethodPost, "/v1/messages", testToken); rec.Code == http.StatusUnauthorized {
		t.Fatal("token client rejected with gateway on")
	}
}

func TestGatewayWithoutAccessLog(t *testing.T) {
	cfg := gatewayConfig(t)
	cfg.AccessLog = false
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if rec := serve(a, http.MethodGet, "/.well-known/oauth-authorization-server", ""); rec.Code != http.StatusOK {
		t.Fatalf("metadata: %d", rec.Code)
	}
}

func TestGatewayOffServesNoGatewayRoutes(t *testing.T) {
	a, err := New(testConfig(t, writePool(t, validPool)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// Without the gateway the path falls to the authenticated proxy.
	if rec := serve(a, http.MethodGet, "/.well-known/oauth-authorization-server", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("gateway route served while off: %d", rec.Code)
	}
}

func TestGatewayFailsStartupWithoutRedis(t *testing.T) {
	cfg := gatewayConfig(t)
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := dead.Addr().String()
	dead.Close()
	cfg.RedisURL = "redis://" + addr
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "redis") {
		t.Fatalf("unreachable redis must fail startup, got %v", err)
	}

	cfg = gatewayConfig(t)
	cfg.RedisURL = "://bad"
	if _, err := New(cfg); err == nil {
		t.Fatal("bad redis URL must fail startup with the gateway on")
	}
}

func TestGatewayBadConfigFailsStartup(t *testing.T) {
	cfg := gatewayConfig(t)
	cfg.Gateway.JWTSecrets = nil
	if _, err := New(cfg); err == nil {
		t.Fatal("missing JWT secret must fail startup")
	}
}

// writeCert writes a self-signed certificate for 127.0.0.1.
func writeCert(t *testing.T) (certPath, keyPath string, pool *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "proem"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	return certPath, keyPath, pool
}

func TestRunServesTLS(t *testing.T) {
	cfg := testConfig(t, writePool(t, validPool))
	var pool *x509.CertPool
	cfg.TLSCert, cfg.TLSKey, pool = writeCert(t)
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Run(ctx) }()

	c := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = c.Get("https://" + cfg.ListenAddr + "/health")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("TLS listener never came up: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health over TLS: %d", resp.StatusCode)
	}
}
