package proxy

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/barockok/proem/internal/client"
)

// stubSessions accepts tokens listed in emails.
type stubSessions map[string]string

func (s stubSessions) VerifySession(token string) (string, error) {
	if email, ok := s[token]; ok {
		return email, nil
	}
	return "", errors.New("invalid")
}

const (
	aliceJWT   = "aaaa.bbbb.cccc"
	erinJWT    = "eeee.ffff.gggg"
	unknownJWT = "uuuu.vvvv.wwww"
)

func sessionRegistry(t *testing.T) stubRegistry {
	t.Helper()
	disabled := false
	reg := &client.Registry{Clients: []client.Client{
		{Name: "alice", Email: "alice@example.com"},
		{Name: "erin", Email: "erin@example.com", Enabled: &disabled},
		{Name: "agent-maria", TokenSHA256: client.HashToken(mariaToken)},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return stubRegistry{reg: reg}
}

func sessionAuth(t *testing.T, seen *string) http.Handler {
	sessions := stubSessions{aliceJWT: "Alice@Example.com", erinJWT: "erin@example.com", unknownJWT: "bob@example.com"}
	return AuthWithSessions(sessionRegistry(t), sessions, testLogger(nil), echoClient(seen))
}

func serveBearer(h http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthAcceptsSessionJWT(t *testing.T) {
	var seen string
	rec := serveBearer(sessionAuth(t, &seen), aliceJWT)
	if rec.Code != http.StatusOK || seen != "alice" {
		t.Fatalf("status %d client %q", rec.Code, seen)
	}
}

func TestAuthSessionRejections(t *testing.T) {
	cases := []struct {
		token    string
		status   int
		wantType string
	}{
		{"xxxx.yyyy.zzzz", http.StatusUnauthorized, "authentication_error"}, // bad signature
		{"eyJhbGciOiJub25lIn0.eyJ9.", http.StatusUnauthorized, "authentication_error"},
		{unknownJWT, http.StatusUnauthorized, "authentication_error"},
		{erinJWT, http.StatusForbidden, "permission_error"},
	}
	for _, tc := range cases {
		var seen string
		rec := serveBearer(sessionAuth(t, &seen), tc.token)
		if rec.Code != tc.status {
			t.Fatalf("%s: status %d", tc.token, rec.Code)
		}
		assertAnthropicError(t, rec, tc.wantType)
		if seen != "" {
			t.Fatalf("%s: reached the handler", tc.token)
		}
	}
}

func TestAuthHashTokensStillWorkWithSessions(t *testing.T) {
	var seen string
	h := sessionAuth(t, &seen)
	if rec := serveBearer(h, mariaToken); rec.Code != http.StatusOK || seen != "agent-maria" {
		t.Fatalf("hash token: %d %q", rec.Code, seen)
	}
	if rec := serveBearer(h, "sk-ant-oat01-unknown"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown hash token: %d", rec.Code)
	}
}

func TestAuthWithoutSessionsTreatsJWTAsHashToken(t *testing.T) {
	var seen string
	h := Auth(sessionRegistry(t), testLogger(nil), echoClient(&seen))
	if rec := serveBearer(h, aliceJWT); rec.Code != http.StatusUnauthorized {
		t.Fatalf("gateway off: %d", rec.Code)
	}
}

func TestLooksLikeJWT(t *testing.T) {
	for tok, want := range map[string]bool{
		"a.b.c": true, "a.b.": true, "a-_.b.c": true,
		"sk-ant-oat01-abc": false, "a.b": false, "a.b.c.d": false, ".b.c": false, "a..c": false, "a.b+.c": false,
	} {
		if looksLikeJWT(tok) != want {
			t.Errorf("%q: want %v", tok, want)
		}
	}
}
