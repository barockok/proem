//go:build e2e

// Package driver runs the SSO gateway end-to-end scenarios against the
// docker-compose stack in e2e/. It plays both parties: Claude Code (discovery,
// device authorization, polling, refresh, API calls) and the browser (confirm
// page, Keycloak login form, callback). Run it with `make e2e`.
package driver

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	proemURL   = env("E2E_PROEM", "https://proem:8443")
	shortURL   = env("E2E_PROEM_SHORT", "https://proem-short:8443")
	legacyURL  = env("E2E_PROEM_LEGACY", "http://proem-legacy:8080")
	metricsURL = env("E2E_PROEM_METRICS", "http://proem:9090/metrics")
	mockURL    = env("E2E_MOCK", "https://mock-upstream:8443")
	kcURL      = env("E2E_KEYCLOAK", "http://keycloak:8080")
	caFile     = env("E2E_CA", "/certs/ca.pem")
	clientsYML = env("E2E_CLIENTS", "/clients/clients.yaml")
	sharedDir  = env("E2E_SHARED", "/shared")

	mockKey     = env("E2E_MOCK_UPSTREAM_KEY", "sk-ant-api03-e2e-mock-upstream")
	legacyToken = "sk-ant-oat01-e2e-legacy-token"

	passwords = map[string]string{"alice": "alice-pw", "bob": "bob-pw", "dave": "dave-pw"}
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

var (
	tlsOnce   sync.Once
	transport *http.Transport
)

// tr trusts the per-run CA, like NODE_EXTRA_CA_CERTS does for Claude Code.
func tr(t *testing.T) *http.Transport {
	tlsOnce.Do(func() {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatalf("read CA: %v", err)
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	})
	return transport
}

// api is Claude Code's HTTP client: no cookies, no redirects.
func api(t *testing.T) *http.Client {
	return &http.Client{
		Transport:     tr(t),
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// browser is a cookie-holding client that follows redirects.
func browser(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: tr(t), Jar: jar, Timeout: 30 * time.Second}
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode %s: %v", resp.Request.URL, err)
	}
}

// --- Claude Code side ------------------------------------------------------

type metadata struct {
	Issuer                      string   `json:"issuer"`
	DeviceAuthorizationEndpoint string   `json:"device_authorization_endpoint"`
	TokenEndpoint               string   `json:"token_endpoint"`
	GrantTypes                  []string `json:"grant_types_supported"`
	GatewayProtocolVersion      int      `json:"gateway_protocol_version"`
}

func discover(t *testing.T, base string) metadata {
	t.Helper()
	resp, err := api(t).Get(base + "/.well-known/oauth-authorization-server")
	if err != nil {
		t.Fatal(err)
	}
	var m metadata
	decode(t, resp, &m)
	return m
}

type device struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func startDevice(t *testing.T, base string) device {
	t.Helper()
	m := discover(t, base)
	// Claude Code posts an empty form body.
	resp, err := api(t).Post(m.DeviceAuthorizationEndpoint, "application/x-www-form-urlencoded", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("device authorization: %d %s", resp.StatusCode, readBody(t, resp))
	}
	var d device
	decode(t, resp, &d)
	return d
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	status       int
}

func token(t *testing.T, base string, form url.Values) tokens {
	t.Helper()
	resp, err := api(t).PostForm(base+"/oauth/token", form)
	if err != nil {
		t.Fatal(err)
	}
	var out tokens
	out.status = resp.StatusCode
	decode(t, resp, &out)
	return out
}

func poll(t *testing.T, base, deviceCode string) tokens {
	t.Helper()
	return token(t, base, url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {deviceCode}})
}

func refresh(t *testing.T, base, rt string) tokens {
	t.Helper()
	return token(t, base, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}})
}

// pollUntil polls at the advertised interval until the grant leaves
// authorization_pending, honouring slow_down like Claude Code does.
func pollUntil(t *testing.T, base string, d device) tokens {
	t.Helper()
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(interval)
		out := poll(t, base, d.DeviceCode)
		switch out.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		}
		return out
	}
	t.Fatal("grant never left authorization_pending")
	return tokens{}
}

// --- browser side ----------------------------------------------------------

var (
	csrfRE     = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	selectedRE = regexp.MustCompile(`name="selected" value="([^"]*)"`)
	kcFormRE   = regexp.MustCompile(`(?s)<form[^>]*id="kc-form-login"[^>]*action="([^"]+)"`)
)

// confirm opens the verification link and submits the typed code. It returns
// the page the browser lands on (the Keycloak login page on success).
func confirm(t *testing.T, b *http.Client, link, typed string) (int, string) {
	t.Helper()
	resp, err := b.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	page := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, page
	}
	csrf := csrfRE.FindStringSubmatch(page)
	sel := selectedRE.FindStringSubmatch(page)
	if csrf == nil || sel == nil {
		t.Fatalf("confirm page has no form:\n%s", page)
	}
	u, _ := url.Parse(link)
	u.RawQuery = ""
	resp, err = b.PostForm(u.String(), url.Values{"csrf": {csrf[1]}, "selected": {sel[1]}, "user_code": {typed}})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, readBody(t, resp)
}

// keycloakLogin posts the Keycloak login form and follows the redirect back
// to proem's callback. It returns proem's final page.
func keycloakLogin(t *testing.T, b *http.Client, loginPage, user string) (int, string) {
	t.Helper()
	m := kcFormRE.FindStringSubmatch(loginPage)
	if m == nil {
		t.Fatalf("not a Keycloak login page:\n%s", loginPage)
	}
	resp, err := b.PostForm(html.UnescapeString(m[1]), url.Values{
		"username": {user}, "password": {passwords[user]}, "credentialId": {""},
	})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, readBody(t, resp)
}

// browserLogin runs the browser half for user and returns proem's page.
func browserLogin(t *testing.T, base string, d device, user string) (int, string) {
	t.Helper()
	b := browser(t)
	status, page := confirm(t, b, d.VerificationURIComplete, d.UserCode)
	if status != http.StatusOK {
		t.Fatalf("confirm: %d %s", status, page)
	}
	return keycloakLogin(t, b, page, user)
}

// login runs the whole device flow for user and returns the token pair.
func login(t *testing.T, base, user string) tokens {
	t.Helper()
	d := startDevice(t, base)
	status, page := browserLogin(t, base, d, user)
	if status != http.StatusOK || !strings.Contains(page, "Signed in") {
		t.Fatalf("login as %s: %d %s", user, status, page)
	}
	out := pollUntil(t, base, d)
	if out.status != http.StatusOK || out.AccessToken == "" {
		t.Fatalf("token for %s: %+v", user, out)
	}
	return out
}

// --- runtime ---------------------------------------------------------------

func messages(t *testing.T, base, bearer string, stream bool) (int, string) {
	t.Helper()
	body := fmt.Sprintf(`{"model":"claude-e2e","max_tokens":16,"stream":%t,"messages":[{"role":"user","content":"hi"}]}`, stream)
	req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := api(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, readBody(t, resp)
}

type upstreamRequest struct {
	Path          string `json:"path"`
	Authorization string `json:"authorization"`
	APIKey        string `json:"x_api_key"`
	Stream        bool   `json:"stream"`
}

func upstreamRequests(t *testing.T) []upstreamRequest {
	t.Helper()
	resp, err := api(t).Get(mockURL + "/_requests")
	if err != nil {
		t.Fatal(err)
	}
	var out []upstreamRequest
	decode(t, resp, &out)
	return out
}

func resetUpstream(t *testing.T) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, mockURL+"/_requests", nil)
	resp, err := api(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func metricsText(t *testing.T) string {
	t.Helper()
	resp, err := http.Get(metricsURL)
	if err != nil {
		t.Fatal(err)
	}
	return readBody(t, resp)
}

// eventually retries fn until it returns true or the deadline passes.
func eventually(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// replaceClients writes body to clients.yaml by temp file and rename, the way
// editors and Kubernetes ConfigMaps replace files.
func replaceClients(t *testing.T, body string) {
	t.Helper()
	tmp := filepath.Join(filepath.Dir(clientsYML), ".clients.yaml.tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, clientsYML); err != nil {
		t.Fatal(err)
	}
}

// --- Keycloak admin --------------------------------------------------------

func kcAdmin(t *testing.T) string {
	t.Helper()
	resp, err := http.PostForm(kcURL+"/realms/master/protocol/openid-connect/token", url.Values{
		"grant_type": {"password"}, "client_id": {"admin-cli"},
		"username": {env("E2E_KC_ADMIN", "admin")}, "password": {env("E2E_KC_ADMIN_PASSWORD", "admin")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	decode(t, resp, &out)
	if out.AccessToken == "" {
		t.Fatal("no Keycloak admin token")
	}
	return out.AccessToken
}

func setUserEnabled(t *testing.T, email string, enabled bool) {
	t.Helper()
	tok := kcAdmin(t)
	req, _ := http.NewRequest(http.MethodGet, kcURL+"/admin/realms/proem/users?exact=true&email="+url.QueryEscape(email), nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var users []struct {
		ID string `json:"id"`
	}
	decode(t, resp, &users)
	if len(users) != 1 {
		t.Fatalf("Keycloak user %s: found %d", email, len(users))
	}
	body := fmt.Sprintf(`{"enabled":%t}`, enabled)
	req, _ = http.NewRequest(http.MethodPut, kcURL+"/admin/realms/proem/users/"+users[0].ID, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("update Keycloak user: %d %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
}

// --- scenarios -------------------------------------------------------------

// 1. Discovery.
func TestDiscovery(t *testing.T) {
	m := discover(t, proemURL)
	for name, endpoint := range map[string]string{"device": m.DeviceAuthorizationEndpoint, "token": m.TokenEndpoint} {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme+"://"+u.Host != proemURL {
			t.Fatalf("%s endpoint %q is not same-origin with %s", name, endpoint, proemURL)
		}
	}
	if m.GatewayProtocolVersion != 1 {
		t.Fatalf("gateway_protocol_version = %d", m.GatewayProtocolVersion)
	}
	resp, err := api(t).Get(proemURL + "/managed/settings")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("managed settings: %d", resp.StatusCode)
	}
}

// 2, 3. Happy path and runtime.
func TestHappyPathAndRuntime(t *testing.T) {
	d := startDevice(t, proemURL)
	if out := poll(t, proemURL, d.DeviceCode); out.Error != "authorization_pending" {
		t.Fatalf("before approval: %+v", out)
	}
	status, page := browserLogin(t, proemURL, d, "alice")
	if status != http.StatusOK || !strings.Contains(page, "Signed in as alice@example.com") {
		t.Fatalf("callback page: %d %s", status, page)
	}
	out := pollUntil(t, proemURL, d)
	if out.status != http.StatusOK || out.AccessToken == "" || !strings.HasPrefix(out.RefreshToken, "prt1.") {
		t.Fatalf("token pair: %+v", out)
	}
	time.Sleep(time.Duration(d.Interval) * time.Second)
	if again := poll(t, proemURL, d.DeviceCode); again.Error != "invalid_grant" {
		t.Fatalf("second exchange must fail: %+v", again)
	}

	resetUpstream(t)
	for _, stream := range []bool{false, true} {
		status, body := messages(t, proemURL, out.AccessToken, stream)
		if status != http.StatusOK {
			t.Fatalf("messages stream=%t: %d %s", stream, status, body)
		}
		if stream && !strings.Contains(body, "message_stop") {
			t.Fatalf("SSE body incomplete: %s", body)
		}
	}
	reqs := upstreamRequests(t)
	if len(reqs) != 2 {
		t.Fatalf("upstream saw %d requests, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.APIKey != mockKey {
			t.Fatalf("upstream got x-api-key %q, want the pool member's credential", r.APIKey)
		}
		if strings.Contains(r.Authorization, out.AccessToken) || r.Authorization != "" {
			t.Fatalf("caller credential leaked upstream: %q", r.Authorization)
		}
	}
	if !strings.Contains(metricsText(t), `client="alice"`) {
		t.Fatal(`metrics have no client="alice" label`)
	}
}

// 4. Polling faster than the interval.
func TestSlowDown(t *testing.T) {
	d := startDevice(t, proemURL)
	poll(t, proemURL, d.DeviceCode)
	if out := poll(t, proemURL, d.DeviceCode); out.Error != "slow_down" {
		t.Fatalf("fast poll: %+v", out)
	}
}

// 5. Refresh.
func TestRefresh(t *testing.T) {
	first := login(t, proemURL, "alice")
	out := refresh(t, proemURL, first.RefreshToken)
	if out.status != http.StatusOK || out.AccessToken == "" {
		t.Fatalf("refresh: %+v", out)
	}
	if status, body := messages(t, proemURL, out.AccessToken, false); status != http.StatusOK {
		t.Fatalf("refreshed JWT: %d %s", status, body)
	}
	if out.RefreshToken != "" {
		if again := refresh(t, proemURL, out.RefreshToken); again.status != http.StatusOK {
			t.Fatalf("refresh with the new blob: %+v", again)
		}
	}
}

// 6. Revocation through clients.yaml, surviving an atomic replace.
func TestRevocationViaClientsYAML(t *testing.T) {
	original, err := os.ReadFile(clientsYML)
	if err != nil {
		t.Fatal(err)
	}
	pair := login(t, proemURL, "alice")
	without := regexp.MustCompile(`(?m)^  - name: alice\n    email: alice@example.com\n`).ReplaceAllString(string(original), "")
	if without == string(original) {
		t.Fatal("could not remove alice from clients.yaml")
	}
	replaceClients(t, without)
	t.Cleanup(func() { replaceClients(t, string(original)) })

	eventually(t, "alice to be rejected", func() bool {
		status, _ := messages(t, proemURL, pair.AccessToken, false)
		return status == http.StatusUnauthorized
	})
	if out := refresh(t, proemURL, pair.RefreshToken); out.Error != "invalid_grant" {
		t.Fatalf("refresh after removal: %+v", out)
	}

	replaceClients(t, string(original))
	eventually(t, "alice to be accepted again", func() bool {
		status, _ := messages(t, proemURL, pair.AccessToken, false)
		return status == http.StatusOK
	})
}

// 7. Revocation at the IdP.
func TestRevocationViaIdP(t *testing.T) {
	pair := login(t, proemURL, "alice")
	setUserEnabled(t, "alice@example.com", false)
	t.Cleanup(func() { setUserEnabled(t, "alice@example.com", true) })
	if out := refresh(t, proemURL, pair.RefreshToken); out.Error != "invalid_grant" {
		t.Fatalf("refresh for a disabled IdP user: %+v", out)
	}
}

// 8. Denials.
func TestDenials(t *testing.T) {
	for user, want := range map[string]string{
		"bob":  "not registered with proem",
		"dave": "not verified",
	} {
		t.Run(user, func(t *testing.T) {
			d := startDevice(t, proemURL)
			status, page := browserLogin(t, proemURL, d, user)
			if status != http.StatusForbidden || !strings.Contains(page, want) {
				t.Fatalf("page: %d %s", status, page)
			}
			if out := pollUntil(t, proemURL, d); out.Error != "access_denied" {
				t.Fatalf("poll: %+v", out)
			}
		})
	}
}

// 9. A grant left pending past its TTL.
func TestGrantExpiry(t *testing.T) {
	d := startDevice(t, shortURL)
	time.Sleep(time.Duration(d.ExpiresIn+1) * time.Second)
	if out := poll(t, shortURL, d.DeviceCode); out.Error != "expired_token" {
		t.Fatalf("expired grant: %+v", out)
	}
}

// 10. Legacy token clients, with and without the gateway.
func TestLegacyTokenClient(t *testing.T) {
	for _, base := range []string{proemURL, legacyURL} {
		if status, body := messages(t, base, legacyToken, false); status != http.StatusOK {
			t.Fatalf("%s: legacy token: %d %s", base, status, body)
		}
	}
}

// 11. Hardening.
func TestHardening(t *testing.T) {
	pair := login(t, proemURL, "alice")
	parts := strings.Split(pair.AccessToken, ".")
	b64 := base64.RawURLEncoding

	claims, _ := b64.DecodeString(parts[1])
	evil := strings.Replace(string(claims), "alice@example.com", "dave@example.com", 1)
	tampered := parts[0] + "." + b64.EncodeToString([]byte(evil)) + "." + parts[2]

	key := make([]byte, 32)
	_, _ = rand.Read(key)
	hdr := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(hdr + "." + parts[1]))
	unknownKey := hdr + "." + parts[1] + "." + b64.EncodeToString(mac.Sum(nil))

	none := b64.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + "."

	for name, jwt := range map[string]string{"tampered": tampered, "unknown key": unknownKey, "alg none": none} {
		if status, _ := messages(t, proemURL, jwt, false); status != http.StatusUnauthorized {
			t.Fatalf("%s JWT: %d", name, status)
		}
	}

	blob := []byte(pair.RefreshToken)
	blob[len(blob)-2] ^= 1
	if out := refresh(t, proemURL, string(blob)); out.Error != "invalid_grant" {
		t.Fatalf("tampered refresh blob: %+v", out)
	}

	// Wrong typed user code.
	d := startDevice(t, proemURL)
	status, page := confirm(t, browser(t), d.VerificationURIComplete, "BBBB-BBBB")
	if status != http.StatusBadRequest || !strings.Contains(page, "does not match") {
		t.Fatalf("wrong typed code: %d %s", status, page)
	}

	// Wrong state on the callback: stop at Keycloak, then forge the return.
	d = startDevice(t, proemURL)
	b := browser(t)
	status, page = confirm(t, b, d.VerificationURIComplete, d.UserCode)
	if status != http.StatusOK || kcFormRE.FindStringSubmatch(page) == nil {
		t.Fatalf("expected the Keycloak login page: %d", status)
	}
	resp, err := b.Get(proemURL + "/oauth/callback?state=forged&code=forged")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged state: %d %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
}

// 12. Concurrent consumption of one approved device code.
func TestConcurrentConsume(t *testing.T) {
	d := startDevice(t, proemURL)
	if status, page := browserLogin(t, proemURL, d, "alice"); status != http.StatusOK {
		t.Fatalf("login: %d %s", status, page)
	}
	var wg sync.WaitGroup
	results := make([]tokens, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = poll(t, proemURL, d.DeviceCode)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, r := range results {
		if r.AccessToken != "" {
			won++
		} else if r.Error != "invalid_grant" {
			t.Fatalf("loser got %+v", r)
		}
	}
	if won != 1 {
		t.Fatalf("%d polls received tokens, want exactly 1", won)
	}
}

// 13. session-max, then the /device rate limit, on the short-lived instance.
func TestSessionMaxAndRateLimit(t *testing.T) {
	pair := login(t, shortURL, "alice")
	max, _ := time.ParseDuration(env("E2E_SHORT_SESSION_MAX", "20s"))
	time.Sleep(max + 2*time.Second)
	if out := refresh(t, shortURL, pair.RefreshToken); out.Error != "invalid_grant" {
		t.Fatalf("refresh after session-max: %+v", out)
	}

	b := browser(t)
	limited := false
	for i := 0; i < 20 && !limited; i++ {
		resp, err := b.Get(shortURL + "/device")
		if err != nil {
			t.Fatal(err)
		}
		limited = resp.StatusCode == http.StatusTooManyRequests
		resp.Body.Close()
	}
	if !limited {
		t.Fatal("/device was never rate limited")
	}
}

// Last: hand a fresh session JWT to the real Claude Code smoke test.
func TestZZWriteTokenForClaudeSmoke(t *testing.T) {
	pair := login(t, proemURL, "alice")
	if err := os.WriteFile(filepath.Join(sharedDir, "jwt"), []byte(pair.AccessToken), 0o644); err != nil {
		t.Fatal(err)
	}
}
