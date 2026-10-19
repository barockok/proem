package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

var (
	testJWTSecret  = strings.Repeat("s", 32)
	testRefreshKey = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
)

func setGatewayEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OIDC_CLIENT_ID", "proem")
	t.Setenv("OIDC_CLIENT_SECRET", "secret")
	t.Setenv("PROEM_JWT_SECRET", testJWTSecret+","+strings.Repeat("t", 40))
	t.Setenv("PROEM_REFRESH_KEY", testRefreshKey)
}

func gatewayArgs(extra ...string) []string {
	return append([]string{"--public-url", "https://proem.example.com", "--oidc-issuer", "https://idp.example.com"}, extra...)
}

func TestGatewayDisabledByDefault(t *testing.T) {
	cfg, err := ParseArgs("proem", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.Enabled() {
		t.Fatal("gateway must be off without --public-url")
	}
}

func TestGatewayParsesFlagsAndEnv(t *testing.T) {
	setGatewayEnv(t)
	t.Setenv("OIDC_SCOPES", "openid,email,offline_access")
	t.Setenv("PROEM_SESSION_TTL", "30m")
	cfg, err := ParseArgs("proem", gatewayArgs(
		"--oidc-auth-params", "access_type=offline, prompt=consent",
		"--oidc-allowed-domain", "example.com",
		"--session-max", "48h",
		"--device-rate-limit", "5/s",
		"--device-grant-ttl", "2s",
		"--tls-cert", "c.pem", "--tls-key", "k.pem",
	))
	if err != nil {
		t.Fatal(err)
	}
	gw := cfg.Gateway
	if !gw.Enabled() || gw.OIDCClientID != "proem" || gw.OIDCClientSecret != "secret" {
		t.Fatalf("gateway not loaded: %+v", gw)
	}
	if strings.Join(gw.OIDCScopes, " ") != "openid email offline_access" {
		t.Fatalf("scopes %v", gw.OIDCScopes)
	}
	if gw.OIDCAuthParams["access_type"] != "offline" || gw.OIDCAuthParams["prompt"] != "consent" {
		t.Fatalf("auth params %v", gw.OIDCAuthParams)
	}
	if gw.SessionTTL != 30*time.Minute || gw.SessionMax != 48*time.Hour || gw.DeviceGrantTTL != 2*time.Second {
		t.Fatalf("durations %+v", gw)
	}
	if gw.DeviceRateLimit != 5 || gw.DeviceRateWindow != time.Second {
		t.Fatalf("rate %d/%s", gw.DeviceRateLimit, gw.DeviceRateWindow)
	}
	if len(gw.JWTSecrets) != 2 || len(gw.RefreshKeys) != 1 || len(gw.RefreshKeys[0]) != 32 {
		t.Fatalf("keys %d %d", len(gw.JWTSecrets), len(gw.RefreshKeys))
	}
	if gw.OIDCAllowedDomain != "example.com" || cfg.TLSCert != "c.pem" || cfg.TLSKey != "k.pem" {
		t.Fatalf("domain/tls %+v", cfg)
	}
}

func TestGatewayPublicURLFromEnv(t *testing.T) {
	setGatewayEnv(t)
	t.Setenv("PROEM_PUBLIC_URL", "http://127.0.0.1:8080")
	t.Setenv("OIDC_ISSUER", "https://idp.example.com")
	cfg, err := ParseArgs("proem", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Gateway.PublicURL != "http://127.0.0.1:8080" {
		t.Fatalf("public url %q", cfg.Gateway.PublicURL)
	}
}

func TestGatewayRateUnits(t *testing.T) {
	for in, want := range map[string]time.Duration{"3/sec": time.Second, "3/m": time.Minute, "3/hour": time.Hour, "3/h": time.Hour} {
		n, d, err := parseRate(in)
		if err != nil || n != 3 || d != want {
			t.Fatalf("%s: %d %s %v", in, n, d, err)
		}
	}
}

func TestGatewayRejectsBadSettings(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		args []string
	}{
		{"http public url", nil, []string{"--public-url", "http://proem.example.com", "--oidc-issuer", "https://idp"}},
		{"relative public url", nil, []string{"--public-url", "proem", "--oidc-issuer", "https://idp"}},
		{"no issuer", nil, []string{"--public-url", "https://proem.example.com"}},
		{"no client id", map[string]string{"OIDC_CLIENT_ID": ""}, gatewayArgs()},
		{"no jwt secret", map[string]string{"PROEM_JWT_SECRET": ""}, gatewayArgs()},
		{"short jwt secret", map[string]string{"PROEM_JWT_SECRET": "short"}, gatewayArgs()},
		{"no refresh key", map[string]string{"PROEM_REFRESH_KEY": ""}, gatewayArgs()},
		{"short refresh key", map[string]string{"PROEM_REFRESH_KEY": base64.StdEncoding.EncodeToString([]byte("x"))}, gatewayArgs()},
		{"refresh key not base64", map[string]string{"PROEM_REFRESH_KEY": "!!!"}, gatewayArgs()},
		{"bad session ttl env", map[string]string{"PROEM_SESSION_TTL": "soon"}, gatewayArgs()},
		{"bad session max env", map[string]string{"PROEM_SESSION_MAX": "later"}, gatewayArgs()},
		{"ttl longer than max", nil, gatewayArgs("--session-ttl", "2h", "--session-max", "1h")},
		{"zero grant ttl", nil, gatewayArgs("--device-grant-ttl", "0s")},
		{"no redis", nil, gatewayArgs("--redis-url", "")},
		{"bad auth params", nil, gatewayArgs("--oidc-auth-params", "novalue")},
		{"bad rate", nil, gatewayArgs("--device-rate-limit", "fast")},
		{"bad rate unit", nil, gatewayArgs("--device-rate-limit", "10/day")},
		{"tls cert without key", nil, []string{"--tls-cert", "c.pem"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGatewayEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := ParseArgs("proem", tc.args); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true, "proem": false, "10.0.0.1": false} {
		if isLoopback(host) != want {
			t.Fatalf("%s: want %v", host, want)
		}
	}
}
