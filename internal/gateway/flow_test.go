package gateway

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barockok/proem/internal/client"
)

func TestMetadataIsSameOrigin(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.srv.URL + metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	for _, k := range []string{"device_authorization_endpoint", "token_endpoint", "issuer"} {
		if s, _ := m[k].(string); !strings.HasPrefix(s, h.srv.URL) {
			t.Fatalf("%s %q is not same-origin with %s", k, s, h.srv.URL)
		}
	}
	if m["gateway_protocol_version"] != float64(1) {
		t.Fatalf("gateway_protocol_version: %v", m["gateway_protocol_version"])
	}
}

func TestDeviceAuthorizationShape(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	if len(d.DeviceCode) < 40 || len(d.UserCode) != 9 || d.UserCode[4] != '-' {
		t.Fatalf("codes: %+v", d)
	}
	for _, r := range strings.ReplaceAll(d.UserCode, "-", "") {
		if !strings.ContainsRune(userCodeAlphabet, r) {
			t.Fatalf("user code %q uses %q", d.UserCode, r)
		}
	}
	if d.VerificationURI != h.srv.URL+"/device" ||
		d.VerificationURIComplete != h.srv.URL+"/device?user_code="+d.UserCode {
		t.Fatalf("verification uris: %+v", d)
	}
	if d.ExpiresIn != 600 || d.Interval != 5 {
		t.Fatalf("expires/interval: %+v", d)
	}
}

func TestFullDeviceFlow(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()

	if code, tr := h.poll(d.DeviceCode); code != http.StatusBadRequest || tr.Error != errAuthorizationPending {
		t.Fatalf("before approval: %d %+v", code, tr)
	}

	b := newBrowser(t)
	status, body := b.confirm(d.VerificationURIComplete, strings.ToLower(d.UserCode))
	if status != http.StatusOK || !strings.Contains(body, "Signed in as alice@example.com") {
		t.Fatalf("callback page: %d %s", status, body)
	}
	auth := h.idp.lastAuthorize
	if auth.Get("nonce") == "" || auth.Get("state") == "" || auth.Get("code_challenge") == "" ||
		auth.Get("prompt") != "consent" || auth.Get("redirect_uri") != h.srv.URL+callbackPath {
		t.Fatalf("authorize request missing parameters: %v", auth)
	}

	h.advance(6 * time.Second)
	code, tr := h.poll(d.DeviceCode)
	if code != http.StatusOK || tr.AccessToken == "" || !strings.HasPrefix(tr.RefreshToken, refreshPrefix) ||
		tr.TokenType != "Bearer" || tr.ExpiresIn != 3600 {
		t.Fatalf("token: %d %+v", code, tr)
	}
	c, err := h.gw.Sessions().Verify(tr.AccessToken)
	if err != nil || c.Email != "alice@example.com" || c.Subject != "sub-alice" || c.Issuer != h.srv.URL {
		t.Fatalf("issued JWT: %+v %v", c, err)
	}
	if strings.Contains(tr.RefreshToken, "rt-") {
		t.Fatal("IdP refresh token must not be visible in the refresh blob")
	}

	if code, tr := h.poll(d.DeviceCode); code != http.StatusBadRequest || tr.Error != errInvalidGrant {
		t.Fatalf("second exchange must fail: %d %+v", code, tr)
	}
	// The user code is single use.
	if status, _ := newBrowser(t).get(d.VerificationURIComplete); status != http.StatusNotFound {
		t.Fatalf("used user code must be gone: %d", status)
	}
	// The encrypted IdP refresh token is removed from Redis on consume.
	raw, _ := h.mr.Get(deviceKeyPrefix + hashDeviceCode(d.DeviceCode))
	if strings.Contains(raw, refreshPrefix) {
		t.Fatalf("consumed grant still holds the refresh blob: %s", raw)
	}
}

func TestPollSlowDown(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	h.poll(d.DeviceCode)
	if code, tr := h.poll(d.DeviceCode); code != http.StatusBadRequest || tr.Error != errSlowDown {
		t.Fatalf("fast poll: %d %+v", code, tr)
	}
	h.advance(5 * time.Second)
	if _, tr := h.poll(d.DeviceCode); tr.Error != errAuthorizationPending {
		t.Fatalf("poll after interval: %+v", tr)
	}
}

func TestGrantExpires(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.GrantTTL = 2 * time.Second })
	d := h.startDevice()
	h.advance(3 * time.Second)
	if code, tr := h.poll(d.DeviceCode); code != http.StatusBadRequest || tr.Error != errExpiredToken {
		t.Fatalf("expired grant: %d %+v", code, tr)
	}
	if status, _ := newBrowser(t).get(d.VerificationURIComplete); status != http.StatusNotFound {
		t.Fatalf("expired user code: %d", status)
	}
}

func TestDeniedLogins(t *testing.T) {
	cases := []struct {
		name string
		user fakeUser
		cfg  func(*Config)
		want string
	}{
		{"not registered", fakeUser{Sub: "s-bob", Email: "bob@example.com", EmailVerified: true}, nil, "not registered with proem"},
		{"disabled client", fakeUser{Sub: "s-erin", Email: "erin@example.com", EmailVerified: true}, nil, "not registered with proem"},
		{"email not verified", fakeUser{Sub: "s-dave", Email: "dave@example.com", EmailVerified: false}, nil, "not verified"},
		{"email_verified missing", fakeUser{Sub: "s-dave", Email: "dave@example.com"}, nil, "not verified"},
		{"no email", fakeUser{Sub: "s-x", EmailVerified: true}, nil, "did not return an email"},
		{"wrong domain", fakeUser{Sub: "s-alice", Email: "alice@example.com", EmailVerified: true}, func(c *Config) { c.AllowedDomain = "corp.example" }, "allowed domain"},
		{"wrong hd", fakeUser{Sub: "s-alice", Email: "alice@example.com", EmailVerified: "true", HD: "other.example"}, func(c *Config) { c.AllowedDomain = "example.com" }, "allowed domain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mut []func(*Config)
			if tc.cfg != nil {
				mut = append(mut, tc.cfg)
			}
			h := newHarness(t, mut...)
			h.idp.setUser(tc.user)
			d := h.startDevice()
			status, body := newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)
			if status != http.StatusForbidden || !strings.Contains(body, tc.want) {
				t.Fatalf("page: %d %s", status, body)
			}
			if code, tr := h.poll(d.DeviceCode); code != http.StatusBadRequest || tr.Error != errAccessDenied {
				t.Fatalf("poll: %d %+v", code, tr)
			}
		})
	}
}

func TestAllowedDomainAndHostedDomainPass(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AllowedDomain = "Example.com" })
	h.idp.setUser(fakeUser{Sub: "s-alice", Email: "alice@example.com", EmailVerified: "true", HD: "example.com"})
	h.login()
}

func TestIdPErrorOnAuthorizeDeniesGrant(t *testing.T) {
	h := newHarness(t)
	h.idp.set(func(f *fakeIdP) { f.authError = "access_denied" })
	d := h.startDevice()
	status, body := newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)
	if status != http.StatusForbidden || !strings.Contains(body, "cancelled") {
		t.Fatalf("page: %d %s", status, body)
	}
	if _, tr := h.poll(d.DeviceCode); tr.Error != errAccessDenied {
		t.Fatalf("poll: %+v", tr)
	}
}

func TestWrongNonceIsRejected(t *testing.T) {
	h := newHarness(t)
	h.idp.set(func(f *fakeIdP) { f.badNonce = true })
	d := h.startDevice()
	status, body := newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)
	if status != http.StatusForbidden || !strings.Contains(body, "could not be verified") {
		t.Fatalf("page: %d %s", status, body)
	}
	if _, tr := h.poll(d.DeviceCode); tr.Error != errAccessDenied {
		t.Fatalf("poll: %+v", tr)
	}
}

func TestIdPDownDuringCodeExchange(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	b := newBrowser(t)
	// Load discovery first, then take the token endpoint down.
	status, body := b.get(d.VerificationURIComplete)
	if status != http.StatusOK {
		t.Fatal(body)
	}
	csrf := csrfField.FindStringSubmatch(body)[1]
	h.idp.set(func(f *fakeIdP) { f.down = true })
	status, body = b.post(h.srv.URL+devicePath, url.Values{"csrf": {csrf}, "selected": {d.UserCode}, "user_code": {d.UserCode}})
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "cannot be reached") {
		t.Fatalf("page: %d %s", status, body)
	}
	if _, tr := h.poll(d.DeviceCode); tr.Error != errAuthorizationPending {
		t.Fatalf("grant must stay pending: %+v", tr)
	}
}

func TestWrongTypedCode(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	other := h.startDevice()
	b := newBrowser(t)
	status, body := b.confirm(d.VerificationURIComplete, other.UserCode)
	if status != http.StatusBadRequest || !strings.Contains(body, "does not match") {
		t.Fatalf("page: %d %s", status, body)
	}
	if !strings.Contains(h.logs.String(), "device_code_mismatch") {
		t.Fatal("mismatch must be logged")
	}
	// An empty typed code is a mismatch too.
	if status, _ := b.confirm(d.VerificationURIComplete, ""); status != http.StatusBadRequest {
		t.Fatalf("empty code: %d", status)
	}
}

func TestTypedCodeWithoutLink(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	status, body := newBrowser(t).confirm(d.VerificationURI, d.UserCode)
	if status != http.StatusOK || !strings.Contains(body, "Signed in") {
		t.Fatalf("page: %d %s", status, body)
	}
	// An unknown typed code without a link.
	status, body = newBrowser(t).confirm(d.VerificationURI, "BBBB-BBBB")
	if status != http.StatusNotFound {
		t.Fatalf("unknown typed code: %d %s", status, body)
	}
}

func TestConfirmPageShowsRequestAndWarning(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	status, body := newBrowser(t).get(d.VerificationURIComplete)
	if status != http.StatusOK {
		t.Fatal(status)
	}
	for _, want := range []string{"If someone sent you this link, close this page", "127.0.0.1", "Request started", d.UserCode} {
		if !strings.Contains(body, want) {
			t.Fatalf("confirm page missing %q:\n%s", want, body)
		}
	}
	if status, _ := newBrowser(t).get(h.srv.URL + "/device?user_code=ZZZZ-ZZZZ"); status != http.StatusNotFound {
		t.Fatalf("unknown code page: %d", status)
	}
}

func TestDeviceSubmitRequiresCSRF(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	b := newBrowser(t)
	b.get(d.VerificationURIComplete)
	status, _ := b.post(h.srv.URL+devicePath, url.Values{"csrf": {"forged"}, "user_code": {d.UserCode}})
	if status != http.StatusForbidden {
		t.Fatalf("forged csrf: %d", status)
	}
	// Without the cookie at all.
	status, _ = newBrowser(t).post(h.srv.URL+devicePath, url.Values{"csrf": {"x"}, "user_code": {d.UserCode}})
	if status != http.StatusForbidden {
		t.Fatalf("no csrf cookie: %d", status)
	}
}

func TestDeviceRateLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.RateLimit, c.RateWindow = 3, time.Minute })
	b := newBrowser(t)
	for i := 0; i < 3; i++ {
		if status, _ := b.get(h.srv.URL + devicePath); status != http.StatusOK {
			t.Fatalf("request %d: %d", i, status)
		}
	}
	if status, body := b.get(h.srv.URL + devicePath); status != http.StatusTooManyRequests || !strings.Contains(body, "Too many") {
		t.Fatalf("over limit: %d", status)
	}
	if status, _ := b.post(h.srv.URL+devicePath, url.Values{}); status != http.StatusTooManyRequests {
		t.Fatalf("POST over limit: %d", status)
	}
	h.advance(time.Minute)
	if status, _ := b.get(h.srv.URL + devicePath); status != http.StatusOK {
		t.Fatalf("after window: %d", status)
	}
}

func TestCallbackChecks(t *testing.T) {
	h := newHarness(t)
	// No cookie.
	if status, _ := newBrowser(t).get(h.srv.URL + callbackPath + "?state=x&code=y"); status != http.StatusBadRequest {
		t.Fatalf("no cookie: %d", status)
	}

	// Wrong state: stop at the IdP redirect and tamper with it.
	d := h.startDevice()
	b := newBrowser(t)
	_, body := b.get(d.VerificationURIComplete)
	csrf := csrfField.FindStringSubmatch(body)[1]
	b.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := b.c.PostForm(h.srv.URL+devicePath, url.Values{"csrf": {csrf}, "selected": {d.UserCode}, "user_code": {d.UserCode}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("submit: %d", resp.StatusCode)
	}
	authURL, _ := url.Parse(resp.Header.Get("Location"))
	b.c.CheckRedirect = nil
	if status, body := b.get(h.srv.URL + callbackPath + "?state=wrong&code=x"); status != http.StatusBadRequest || !strings.Contains(body, "does not match") {
		t.Fatalf("wrong state: %d %s", status, body)
	}
	// The real state still completes after a forged one was rejected.
	status, body := b.get(authURL.String())
	if status != http.StatusOK || !strings.Contains(body, "Signed in") {
		t.Fatalf("real callback: %d %s", status, body)
	}
	// Replaying the callback is rejected: the grant is no longer pending.
	cb := h.idp.lastAuthorize
	jar := b.c.Jar
	u, _ := url.Parse(h.srv.URL + callbackPath)
	jar.SetCookies(u, []*http.Cookie{{Name: deviceCookie, Value: hashDeviceCode(d.DeviceCode), Path: callbackPath}})
	if status, _ := b.get(h.srv.URL + callbackPath + "?code=x&state=" + url.QueryEscape(cb.Get("state"))); status != http.StatusBadRequest {
		t.Fatalf("replayed callback: %d", status)
	}
	// A cookie for a grant that no longer exists.
	jar.SetCookies(u, []*http.Cookie{{Name: deviceCookie, Value: "deadbeef", Path: callbackPath}})
	if status, _ := b.get(h.srv.URL + callbackPath + "?code=x&state=y"); status != http.StatusNotFound {
		t.Fatalf("gone grant: %d", status)
	}
}

func TestConcurrentConsume(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)

	const n = 8
	var wg sync.WaitGroup
	results := make(chan tokenResponse, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, tr := h.poll(d.DeviceCode)
			results <- tr
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for tr := range results {
		switch {
		case tr.AccessToken != "":
			winners++
		case tr.Error != errInvalidGrant:
			t.Fatalf("loser got %+v, want invalid_grant", tr)
		}
	}
	if winners != 1 {
		t.Fatalf("%d polls received tokens, want exactly 1", winners)
	}
}

func TestClientRemovedBeforeConsume(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)
	h.reg.set(t, client.Client{Name: "dave", Email: "dave@example.com"})
	if _, tr := h.poll(d.DeviceCode); tr.Error != errAccessDenied {
		t.Fatalf("poll: %+v", tr)
	}
}

func TestNoIdPRefreshTokenOmitsRefresh(t *testing.T) {
	h := newHarness(t)
	h.idp.set(func(f *fakeIdP) { f.noRefreshToken = true })
	tr := h.login()
	if tr.RefreshToken != "" {
		t.Fatalf("no IdP refresh token means no proem refresh token, got %q", tr.RefreshToken)
	}
}

func TestTokenEndpointErrors(t *testing.T) {
	h := newHarness(t)
	if code, tr := h.postToken(url.Values{"grant_type": {"password"}}); code != http.StatusBadRequest || tr.Error != errUnsupportedGrantType {
		t.Fatalf("unsupported: %d %+v", code, tr)
	}
	if code, tr := h.postToken(url.Values{"grant_type": {grantDeviceCode}}); code != http.StatusBadRequest || tr.Error != errInvalidRequest {
		t.Fatalf("missing device_code: %d %+v", code, tr)
	}
	if _, tr := h.poll("never-issued"); tr.Error != errExpiredToken {
		t.Fatalf("unknown device code: %+v", tr)
	}
	resp, err := http.Post(h.srv.URL+tokenPath, "application/x-www-form-urlencoded", strings.NewReader("%zz"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body: %d", resp.StatusCode)
	}
}

func TestRedisDown(t *testing.T) {
	h := newHarness(t)
	tr := h.login()
	h.mr.Close()

	resp, err := http.Post(h.srv.URL+deviceAuthPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("device authorization with redis down: %d", resp.StatusCode)
	}
	if code, out := h.poll("x"); code != http.StatusServiceUnavailable || out.Error != errTemporarilyUnavailable {
		t.Fatalf("poll with redis down: %d %+v", code, out)
	}
	if status, _ := newBrowser(t).get(h.srv.URL + "/device?user_code=BBBB-BBBB"); status != http.StatusServiceUnavailable {
		t.Fatalf("confirm page with redis down: %d", status)
	}
	// Session JWTs are verified without Redis.
	if _, err := h.gw.Sessions().Verify(tr.AccessToken); err != nil {
		t.Fatal(err)
	}
	// Refresh does not touch Redis either.
	if code, _ := h.refreshWith(tr.RefreshToken); code != http.StatusOK {
		t.Fatalf("refresh with redis down: %d", code)
	}
}

func TestStubs(t *testing.T) {
	h := newHarness(t)
	tr := h.login()

	resp, err := http.Get(h.srv.URL + managedPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("managed settings: %d", resp.StatusCode)
	}

	for _, signal := range []string{"metrics", "logs", "traces"} {
		for _, ct := range []string{"application/json", "application/x-protobuf"} {
			req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/"+signal, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
			req.Header.Set("Content-Type", ct)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s %s: %d", signal, ct, resp.StatusCode)
			}
		}
		resp, err := http.Post(h.srv.URL+"/v1/"+signal, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without JWT: %d", signal, resp.StatusCode)
		}
	}
}
