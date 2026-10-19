package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"
)

func newTestStore(t *testing.T) (*grantStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return &grantStore{rdb: rdb, ttl: time.Minute, interval: 5 * time.Second, now: time.Now, userCode: randomUserCode}, mr
}

func TestGrantStoreTransitions(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	if err := s.transition(ctx, "missing", statusPending, "", grant{}); !errors.Is(err, errGrantNotFound) {
		t.Fatalf("missing grant: %v", err)
	}
	dc, uc, err := s.create(ctx, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	g, dcHash, err := s.byUserCode(ctx, uc)
	if err != nil || g.Status != statusPending || g.RequesterIP != "1.2.3.4" || dcHash != hashDeviceCode(dc) {
		t.Fatalf("byUserCode: %+v %v", g, err)
	}
	if err := s.transition(ctx, dcHash, statusApproved, "", g); !errors.Is(err, errGrantState) {
		t.Fatalf("wrong status: %v", err)
	}
	if err := s.startLogin(ctx, dcHash, g, "st", "n", "v"); err != nil {
		t.Fatal(err)
	}
	// startLogin works only once.
	if err := s.startLogin(ctx, dcHash, g, "st2", "n", "v"); !errors.Is(err, errGrantState) {
		t.Fatalf("second startLogin: %v", err)
	}
	g, _ = s.byHash(ctx, dcHash)
	if err := s.finish(ctx, dcHash, grant{State: "other"}, statusApproved); !errors.Is(err, errGrantState) {
		t.Fatalf("finish with wrong state: %v", err)
	}
	if err := s.finish(ctx, dcHash, g, statusApproved); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.byUserCode(ctx, ""); !errors.Is(err, errGrantNotFound) {
		t.Fatal("empty user code must not resolve")
	}

	mr.Set(deviceKeyPrefix+"corrupt", "not json")
	if _, err := s.byHash(ctx, "corrupt"); err == nil {
		t.Fatal("corrupt grant must fail")
	}
	mr.Set(deviceKeyPrefix+hashDeviceCode("corrupt"), "not json")
	if _, _, err := s.poll(ctx, "corrupt"); err == nil {
		t.Fatal("corrupt grant must fail to poll")
	}
	mr.Set(deviceKeyPrefix+hashDeviceCode("weird"), `{"status":"approved","created":"x"}`)
	if _, _, err := s.poll(ctx, "weird"); err == nil {
		t.Fatal("undecodable approved grant must fail")
	}
}

func TestGrantStoreUserCodeCollision(t *testing.T) {
	s, _ := newTestStore(t)
	s.userCode = func() string { return "BBBBBBBB" }
	if _, _, err := s.create(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.create(context.Background(), ""); err == nil {
		t.Fatal("a permanently colliding user code must fail")
	}
}

func TestGrantStoreRedisErrors(t *testing.T) {
	s, mr := newTestStore(t)
	ctx := context.Background()
	mr.Close()
	if _, err := s.byHash(ctx, "x"); err == nil || errors.Is(err, errGrantNotFound) {
		t.Fatalf("byHash: %v", err)
	}
	if err := s.transition(ctx, "x", statusPending, "", grant{}); err == nil {
		t.Fatal("transition must fail")
	}
}

func TestUserCodeFormatting(t *testing.T) {
	if formatUserCode("ABC") != "ABC" || formatUserCode("BCDFGHJK") != "BCDF-GHJK" {
		t.Fatal("format")
	}
	if normalizeUserCode(" bcdf-ghjk ") != "BCDFGHJK" {
		t.Fatal("normalize")
	}
}

func TestRateLimiterSweepsExpiredKeys(t *testing.T) {
	now := time.Now()
	l := newRateLimiter(1, time.Minute, func() time.Time { return now })
	for i := 0; i < maxTrackedKeys; i++ {
		l.allow(fmt.Sprint(i))
	}
	now = now.Add(time.Minute)
	if !l.allow("new") {
		t.Fatal("new key must be allowed")
	}
	if len(l.hits) != 1 {
		t.Fatalf("expired keys must be swept, %d remain", len(l.hits))
	}
}

func TestNewValidation(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	reg := &registry{}
	reg.set(t, defaultClients()...)
	p := NewOIDCProvider(OIDCConfig{})
	good := Config{PublicURL: "https://proem.example.com/", JWTSecrets: [][]byte{testSecret}, RefreshKeys: [][]byte{testKey}}

	if g, err := New(good, p, rdb, reg, nil); err != nil || g.cfg.PublicURL != "https://proem.example.com" {
		t.Fatalf("good config: %v", err)
	}
	bad := map[string]Config{
		"relative url": {PublicURL: "proem", JWTSecrets: good.JWTSecrets, RefreshKeys: good.RefreshKeys},
		"no secret":    {PublicURL: good.PublicURL, RefreshKeys: good.RefreshKeys},
		"no key":       {PublicURL: good.PublicURL, JWTSecrets: good.JWTSecrets},
	}
	for name, cfg := range bad {
		if _, err := New(cfg, p, rdb, reg, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(good, nil, rdb, reg, nil); err == nil {
		t.Error("nil provider accepted")
	}
	if RedirectURL("https://p.example/") != "https://p.example/oauth/callback" {
		t.Error("redirect url")
	}
}

func TestNewRedisClient(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb, err := NewRedisClient("redis://" + mr.Addr() + "/0")
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRedisClient("not a url"); err == nil {
		t.Fatal("bad url accepted")
	}
}

func TestClassify(t *testing.T) {
	rejected := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, ErrorCode: "invalid_grant"}
	if !errors.Is(classify(rejected), errIdPRejected) {
		t.Fatal("4xx must be rejected")
	}
	failed := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 502}}
	if !errors.Is(classify(failed), errIdPUnavailable) {
		t.Fatal("5xx must be unavailable")
	}
	if !errors.Is(classify(errors.New("dial")), errIdPUnavailable) {
		t.Fatal("network errors must be unavailable")
	}
}

func TestEmailVerifiedClaimForms(t *testing.T) {
	for v, want := range map[any]bool{true: true, false: false, "true": true, "TRUE": true, "false": false, 1.0: false} {
		if got := (rawClaims{EmailVerified: v}).identity().EmailVerified; got != want {
			t.Errorf("%v: got %v", v, got)
		}
	}
}

func TestProviderDiscoveryFailure(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	p := NewOIDCProvider(OIDCConfig{Issuer: dead.URL, HTTPClient: http.DefaultClient})
	ctx := context.Background()
	if _, err := p.AuthCodeURL(ctx, "s", "n", "v"); !errors.Is(err, errIdPUnavailable) {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	if _, _, err := p.Exchange(ctx, "c", "v", "n"); !errors.Is(err, errIdPUnavailable) {
		t.Fatalf("Exchange: %v", err)
	}
	if _, _, err := p.Refresh(ctx, "rt"); !errors.Is(err, errIdPUnavailable) {
		t.Fatalf("Refresh: %v", err)
	}
}

func TestProviderUnreachableShowsPage(t *testing.T) {
	h := newHarness(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	h.gw.provider = NewOIDCProvider(OIDCConfig{Issuer: dead.URL})
	d := h.startDevice()
	status, body := newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)
	if status != http.StatusServiceUnavailable || !strings.Contains(body, "cannot be reached") {
		t.Fatalf("page: %d %s", status, body)
	}
}

// stubProvider returns canned results, for paths a real IdP rarely takes.
type stubProvider struct {
	id  Identity
	rt  string
	err error
}

func (s stubProvider) AuthCodeURL(_ context.Context, state, _, _ string) (string, error) {
	return "/oauth/callback?code=c&state=" + url.QueryEscape(state), nil
}
func (s stubProvider) Exchange(context.Context, string, string, string) (Identity, string, error) {
	return s.id, s.rt, s.err
}
func (s stubProvider) Refresh(context.Context, string) (Identity, string, error) {
	return s.id, s.rt, s.err
}

func TestExchangeWithoutIDToken(t *testing.T) {
	h := newHarness(t)
	idp := h.idp
	// An IdP that answers the code exchange without an id_token.
	strip := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			writeJSON(w, http.StatusOK, map[string]any{"access_token": "a", "token_type": "Bearer"})
			return
		}
		idp.srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer strip.Close()
	p := NewOIDCProvider(OIDCConfig{Issuer: idp.issuer(), ClientID: idp.client, ClientSecret: idp.secret})
	op := p.(*oidcProvider)
	if _, err := op.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Point the token endpoint at the stripping server.
	op.cfg.HTTPClient = &http.Client{Transport: rewrite{to: strip.URL}}
	if _, _, err := p.Exchange(context.Background(), "c", "v", "n"); !errors.Is(err, errIdPRejected) {
		t.Fatalf("no id_token: %v", err)
	}
}

// rewrite sends every request to another host.
type rewrite struct{ to string }

func (rw rewrite) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(rw.to)
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestIDTokenFromWrongAudienceIsRejected(t *testing.T) {
	h := newHarness(t)
	h.idp.set(func(f *fakeIdP) { f.client = "proem" })
	p := NewOIDCProvider(OIDCConfig{Issuer: h.idp.issuer(), ClientID: "someone-else"}).(*oidcProvider)
	prov, err := p.discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h.idp.mu.Lock()
	raw := h.idp.tokens(fakeUser{Sub: "s", Email: "a@example.com"}, "n", true)["id_token"].(string)
	h.idp.mu.Unlock()
	if _, _, err := p.verifyIDToken(context.Background(), prov, raw); !errors.Is(err, errIdPRejected) {
		t.Fatalf("wrong audience: %v", err)
	}
}

func TestCallbackGrantVanishes(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	// The grant expires between the IdP leg and the callback.
	h.gw.provider = vanishing{stubProvider{id: Identity{Subject: "s", Email: "bob@example.com", EmailVerified: true}}, h, d.DeviceCode}
	status, _ := newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode)
	if status != http.StatusNotFound {
		t.Fatalf("vanished grant: %d", status)
	}
}

// vanishing deletes the grant during the code exchange.
type vanishing struct {
	stubProvider
	h  *harness
	dc string
}

func (v vanishing) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, string, error) {
	v.h.mr.Del(deviceKeyPrefix + hashDeviceCode(v.dc))
	return v.stubProvider.Exchange(ctx, code, verifier, nonce)
}

func TestCallbackApprovalAfterGrantVanishes(t *testing.T) {
	h := newHarness(t)
	d := h.startDevice()
	h.gw.provider = vanishing{stubProvider{id: Identity{Subject: "s", Email: "alice@example.com", EmailVerified: true}, rt: "rt"}, h, d.DeviceCode}
	if status, _ := newBrowser(t).confirm(d.VerificationURIComplete, d.UserCode); status != http.StatusNotFound {
		t.Fatalf("vanished grant on approval: %d", status)
	}
}

func TestDeviceSubmitMalformedForm(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+devicePath, strings.NewReader("%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed form: %d", resp.StatusCode)
	}
}
