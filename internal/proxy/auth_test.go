package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/barockok/proem/internal/client"
)

const (
	mariaToken = "sk-ant-oat01-maria"
	soraToken  = "sk-ant-oat01-sora"
)

type stubRegistry struct{ reg *client.Registry }

func (s stubRegistry) Active() *client.Registry { return s.reg }

func testRegistry(t *testing.T) stubRegistry {
	t.Helper()
	disabled := false
	reg := &client.Registry{Clients: []client.Client{
		{Name: "agent-maria", TokenSHA256: client.HashToken(mariaToken)},
		{Name: "agent-sora", TokenSHA256: client.HashToken(soraToken), Enabled: &disabled},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	return stubRegistry{reg: reg}
}

// echoClient reports the client name the middleware attached.
func echoClient(seen *string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = ClientName(r.Context())
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthAcceptsBearerToken(t *testing.T) {
	var seen string
	h := Auth(testRegistry(t), echoClient(&seen))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+mariaToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if seen != "agent-maria" {
		t.Fatalf("client name %q", seen)
	}
}

func TestAuthAcceptsApiKeyHeader(t *testing.T) {
	var seen string
	h := Auth(testRegistry(t), echoClient(&seen))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", mariaToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || seen != "agent-maria" {
		t.Fatalf("status %d client %q", rec.Code, seen)
	}
}

func TestAuthBearerIsCaseInsensitive(t *testing.T) {
	var seen string
	h := Auth(testRegistry(t), echoClient(&seen))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "bearer  "+mariaToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || seen != "agent-maria" {
		t.Fatalf("status %d client %q", rec.Code, seen)
	}
}

func TestAuthRejectsMissingCredentials(t *testing.T) {
	h := Auth(testRegistry(t), echoClient(new(string)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	assertAnthropicError(t, rec, "authentication_error")
}

func TestAuthRejectsUnknownToken(t *testing.T) {
	h := Auth(testRegistry(t), echoClient(new(string)))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-ant-oat01-not-issued")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", rec.Code)
	}
	assertAnthropicError(t, rec, "authentication_error")
}

func TestAuthRejectsDisabledClient(t *testing.T) {
	h := Auth(testRegistry(t), echoClient(new(string)))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+soraToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	assertAnthropicError(t, rec, "permission_error")
}

func TestAuthRejectsMalformedAuthorization(t *testing.T) {
	h := Auth(testRegistry(t), echoClient(new(string)))
	for _, v := range []string{"Basic abc123", "Bearer", "   ", mariaToken} {
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		req.Header.Set("Authorization", v)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Authorization %q: status %d", v, rec.Code)
		}
	}
}

// The proxy injects the pool member's credential downstream. If a caller's own
// auth headers survived, an x-api-key would take precedence at Anthropic and
// override the injected OAuth token.
func TestAuthStripsCallerCredentials(t *testing.T) {
	var gotAuth, gotAPIKey string
	h := Auth(testRegistry(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+mariaToken)
	req.Header.Set("x-api-key", "sk-ant-api03-caller-key")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotAuth != "" {
		t.Fatalf("caller Authorization leaked downstream: %q", gotAuth)
	}
	if gotAPIKey != "" {
		t.Fatalf("caller x-api-key leaked downstream: %q", gotAPIKey)
	}
}

func TestAuthPreservesOtherHeaders(t *testing.T) {
	var beta, session string
	h := Auth(testRegistry(t), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		session = r.Header.Get("x-claude-code-session-id")
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+mariaToken)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("x-claude-code-session-id", "sess-1")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if beta != "oauth-2025-04-20" {
		t.Fatalf("beta header dropped: %q", beta)
	}
	if session != "sess-1" {
		t.Fatalf("session header dropped: %q", session)
	}
}

func TestClientNameDefaultsToUnknown(t *testing.T) {
	if got := ClientName(context.Background()); got != client.UnknownClient {
		t.Fatalf("want %q, got %q", client.UnknownClient, got)
	}
	if got := ClientName(withClient(context.Background(), "")); got != client.UnknownClient {
		t.Fatalf("empty name should fall back to unknown, got %q", got)
	}
	if got := ClientName(withClient(context.Background(), "agent-maria")); got != "agent-maria" {
		t.Fatalf("got %q", got)
	}
}

func assertAnthropicError(t *testing.T, rec *httptest.ResponseRecorder, wantType string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("content type %q", ct)
	}
	var body struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Type != "error" || body.Error.Type != wantType {
		t.Fatalf("unexpected error envelope: %s", rec.Body.String())
	}
	if body.Error.Message == "" {
		t.Fatal("error message must not be empty")
	}
}
