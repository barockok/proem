package gateway

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// fakeUser is who the fake IdP signs in at the next authorize request.
type fakeUser struct {
	Sub           string
	Email         string
	EmailVerified any
	HD            string
}

type fakeCode struct {
	user      fakeUser
	nonce     string
	challenge string
}

// fakeIdP is a minimal OpenID Connect provider: discovery, JWKS, authorize,
// token (authorization_code with PKCE, refresh_token) and userinfo.
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	client string
	secret string

	mu        sync.Mutex
	next      fakeUser
	codes     map[string]fakeCode
	refreshes map[string]fakeUser
	access    map[string]fakeUser
	n         int

	// Knobs for refresh behaviour.
	rotate         bool // issue a new refresh token on every refresh
	noRefreshToken bool // omit refresh_token from the code exchange
	noIDOnRefresh  bool // omit id_token on refresh (forces userinfo)
	down           bool // every token call returns 500
	badNonce       bool // sign ID tokens with a wrong nonce
	authError      string
	refreshSub     string // override the sub returned on refresh
	userinfoDown   bool

	lastAuthorize url.Values
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{
		t: t, key: key, client: "proem", secret: "idp-secret",
		codes: map[string]fakeCode{}, refreshes: map[string]fakeUser{}, access: map[string]fakeUser{},
		next: fakeUser{Sub: "sub-alice", Email: "alice@example.com", EmailVerified: true},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/jwks", f.jwks)
	mux.HandleFunc("/authorize", f.authorize)
	mux.HandleFunc("/token", f.token)
	mux.HandleFunc("/userinfo", f.userinfo)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) issuer() string { return f.srv.URL }

func (f *fakeIdP) setUser(u fakeUser) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = u
}

func (f *fakeIdP) set(fn func(f *fakeIdP)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeIdP) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                f.issuer(),
		"authorization_endpoint":                f.issuer() + "/authorize",
		"token_endpoint":                        f.issuer() + "/token",
		"jwks_uri":                              f.issuer() + "/jwks",
		"userinfo_endpoint":                     f.issuer() + "/userinfo",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (f *fakeIdP) jwks(w http.ResponseWriter, r *http.Request) {
	pub := f.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
		"n": b64.EncodeToString(pub.N.Bytes()),
		"e": b64.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (f *fakeIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	f.lastAuthorize = q
	f.n++
	code := fmt.Sprintf("code-%d", f.n)
	f.codes[code] = fakeCode{user: f.next, nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	authErr := f.authError
	f.mu.Unlock()

	if q.Get("client_id") != f.client || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	back, _ := url.Parse(q.Get("redirect_uri"))
	v := url.Values{"state": {q.Get("state")}}
	if authErr != "" {
		v.Set("error", authErr)
	} else {
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	_ = r.ParseForm()
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if id != f.client || secret != f.secret {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "")
		return
	}
	switch r.PostFormValue("grant_type") {
	case "authorization_code":
		c, ok := f.codes[r.PostFormValue("code")]
		delete(f.codes, r.PostFormValue("code"))
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		if !ok || b64.EncodeToString(sum[:]) != c.challenge {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "bad code or verifier")
			return
		}
		nonce := c.nonce
		if f.badNonce {
			nonce = "wrong"
		}
		resp := f.tokens(c.user, nonce, true)
		if f.noRefreshToken {
			delete(resp, "refresh_token")
		}
		writeJSON(w, http.StatusOK, resp)
	case "refresh_token":
		u, ok := f.refreshes[r.PostFormValue("refresh_token")]
		if !ok {
			writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
			return
		}
		if f.refreshSub != "" {
			u.Sub = f.refreshSub
		}
		resp := f.tokens(u, "", !f.noIDOnRefresh)
		if !f.rotate {
			delete(f.refreshes, resp["refresh_token"].(string))
			resp["refresh_token"] = r.PostFormValue("refresh_token")
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

// tokens mints a token response for u. Caller holds f.mu.
func (f *fakeIdP) tokens(u fakeUser, nonce string, withID bool) map[string]any {
	f.n++
	rt := fmt.Sprintf("rt-%d", f.n)
	at := fmt.Sprintf("at-%d", f.n)
	f.refreshes[rt] = u
	f.access[at] = u
	resp := map[string]any{"access_token": at, "token_type": "Bearer", "expires_in": 300, "refresh_token": rt}
	if withID {
		claims := map[string]any{
			"iss": f.issuer(), "aud": f.client, "sub": u.Sub, "email": u.Email,
			"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		}
		if u.EmailVerified != nil {
			claims["email_verified"] = u.EmailVerified
		}
		if u.HD != "" {
			claims["hd"] = u.HD
		}
		if nonce != "" {
			claims["nonce"] = nonce
		}
		resp["id_token"] = f.sign(claims)
	}
	return resp
}

func (f *fakeIdP) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	p, _ := json.Marshal(claims)
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + b64.EncodeToString(sig)
}

func (f *fakeIdP) userinfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.userinfoDown {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}
	u, ok := f.access[bearer(r)]
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sub": u.Sub, "email": u.Email, "email_verified": u.EmailVerified})
}

// revoke drops every refresh token, as disabling a user at the IdP does.
func (f *fakeIdP) revoke() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = map[string]fakeUser{}
}
