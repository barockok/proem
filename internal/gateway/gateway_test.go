package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/barockok/proem/internal/client"
	"github.com/barockok/proem/internal/clientip"
	"github.com/barockok/proem/internal/proxy"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
)

var (
	testSecret = []byte(strings.Repeat("j", 32))
	testKey    = []byte(strings.Repeat("r", 32))
)

// registry is a swappable client registry, like client.Loader.
type registry struct {
	mu  sync.Mutex
	reg *client.Registry
}

func (r *registry) Active() *client.Registry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reg
}

func (r *registry) set(t *testing.T, clients ...client.Client) {
	t.Helper()
	reg := &client.Registry{Clients: clients}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.reg = reg
	r.mu.Unlock()
}

func disabled() *bool { b := false; return &b }

func defaultClients() []client.Client {
	return []client.Client{
		{Name: "alice", Email: "alice@example.com"},
		{Name: "dave", Email: "dave@example.com"},
		{Name: "erin", Email: "erin@example.com", Enabled: disabled()},
	}
}

type harness struct {
	t      *testing.T
	gw     *Gateway
	idp    *fakeIdP
	mr     *miniredis.Miniredis
	srv    *httptest.Server
	reg    *registry
	logs   *bytes.Buffer
	nowMu  sync.Mutex
	nowVal time.Time
}

func (h *harness) now() time.Time {
	h.nowMu.Lock()
	defer h.nowMu.Unlock()
	return h.nowVal
}

// advance moves both the gateway clock and Redis TTLs forward.
func (h *harness) advance(d time.Duration) {
	h.nowMu.Lock()
	h.nowVal = h.nowVal.Add(d)
	h.nowMu.Unlock()
	h.mr.FastForward(d)
}

func newHarness(t *testing.T, mutate ...func(*Config)) *harness {
	t.Helper()
	h := &harness{t: t, idp: newFakeIdP(t), mr: miniredis.RunT(t), reg: &registry{}, logs: &bytes.Buffer{}}
	h.nowVal = time.Now()
	h.reg.set(t, defaultClients()...)

	var handler http.Handler
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(h.srv.Close)

	cfg := Config{PublicURL: h.srv.URL, JWTSecrets: [][]byte{testSecret}, RefreshKeys: [][]byte{testKey}}
	for _, m := range mutate {
		m(&cfg)
	}
	rdb := redis.NewClient(&redis.Options{Addr: h.mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	provider := NewOIDCProvider(OIDCConfig{
		Issuer: h.idp.issuer(), ClientID: h.idp.client, ClientSecret: h.idp.secret,
		RedirectURL: RedirectURL(h.srv.URL), Scopes: []string{"openid", "email"},
		AuthParams: map[string]string{"prompt": "consent"},
	})
	logger := slog.New(slog.NewTextHandler(h.logs, nil))
	gw, err := New(cfg, provider, rdb, h.reg, logger)
	if err != nil {
		t.Fatal(err)
	}
	gw.now = h.now
	h.gw = gw

	resolver, _ := clientip.NewResolver(nil)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler { return proxy.RealIP(resolver, next) })
	gw.Routes(r)
	handler = r
	return h
}

// deviceStart is the Claude Code side of device authorization.
type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func (h *harness) startDevice() deviceStart {
	h.t.Helper()
	resp, err := http.Post(h.srv.URL+deviceAuthPath, "application/x-www-form-urlencoded", nil)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("device authorization: %d", resp.StatusCode)
	}
	var d deviceStart
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		h.t.Fatal(err)
	}
	return d
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
}

func (h *harness) postToken(form url.Values) (int, tokenResponse) {
	h.t.Helper()
	resp, err := http.PostForm(h.srv.URL+tokenPath, form)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	_ = json.NewDecoder(resp.Body).Decode(&tr)
	return resp.StatusCode, tr
}

func (h *harness) poll(deviceCode string) (int, tokenResponse) {
	h.t.Helper()
	return h.postToken(url.Values{"grant_type": {grantDeviceCode}, "device_code": {deviceCode}})
}

func (h *harness) refreshWith(rt string) (int, tokenResponse) {
	h.t.Helper()
	return h.postToken(url.Values{"grant_type": {grantRefreshToken}, "refresh_token": {rt}})
}

// browser is a cookie-holding HTTP client that follows redirects.
type browser struct {
	t *testing.T
	c *http.Client
}

func newBrowser(t *testing.T) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, c: &http.Client{Jar: jar}}
}

func (b *browser) get(u string) (int, string) {
	b.t.Helper()
	resp, err := b.c.Get(u)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (b *browser) post(u string, form url.Values) (int, string) {
	b.t.Helper()
	resp, err := b.c.PostForm(u, form)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

var csrfField = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
var selectedField = regexp.MustCompile(`name="selected" value="([^"]*)"`)

// confirm opens the verification link, types code and follows the IdP
// round trip. It returns the final page.
func (b *browser) confirm(link, typed string) (int, string) {
	b.t.Helper()
	status, body := b.get(link)
	if status != http.StatusOK {
		b.t.Fatalf("confirm page: %d %s", status, body)
	}
	m := csrfField.FindStringSubmatch(body)
	if m == nil {
		b.t.Fatalf("no csrf field in: %s", body)
	}
	sel := selectedField.FindStringSubmatch(body)
	u, _ := url.Parse(link)
	u.RawQuery = ""
	return b.post(u.String(), url.Values{"csrf": {m[1]}, "selected": {sel[1]}, "user_code": {typed}})
}

// login runs the whole device flow for the IdP's current user and returns
// the token pair.
func (h *harness) login() tokenResponse {
	h.t.Helper()
	d := h.startDevice()
	status, body := newBrowser(h.t).confirm(d.VerificationURIComplete, d.UserCode)
	if status != http.StatusOK || !strings.Contains(body, "Signed in") {
		h.t.Fatalf("login page: %d %s", status, body)
	}
	code, tr := h.poll(d.DeviceCode)
	if code != http.StatusOK || tr.AccessToken == "" {
		h.t.Fatalf("token: %d %+v", code, tr)
	}
	return tr
}
