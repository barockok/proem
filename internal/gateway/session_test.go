package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newTestSessions(t *testing.T, secrets ...[]byte) *Sessions {
	t.Helper()
	if len(secrets) == 0 {
		secrets = [][]byte{testSecret}
	}
	s, err := NewSessions("https://proem.example.com", time.Hour, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// forge signs arbitrary header and claims with secret.
func forge(secret []byte, header, claims map[string]any) string {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	signing := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	return signing + "." + b64.EncodeToString(sign(secret, signing))
}

func validClaims() map[string]any {
	now := time.Now()
	return map[string]any{
		"iss": "https://proem.example.com", "aud": Audience, "sub": "s", "email": "a@example.com",
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s := newTestSessions(t)
	tok := s.Issue("sub-1", "a@example.com")
	email, err := s.VerifySession(tok)
	if err != nil || email != "a@example.com" {
		t.Fatalf("verify: %q %v", email, err)
	}
	c, _ := s.Verify(tok)
	if c.Subject != "sub-1" || c.Audience != Audience || c.Expiry-c.IssuedAt != 3600 {
		t.Fatalf("claims %+v", c)
	}
	if s.TTL() != time.Hour {
		t.Fatal("ttl")
	}
}

func TestSessionExpiry(t *testing.T) {
	s := newTestSessions(t)
	tok := s.Issue("s", "a@example.com")
	base := time.Now()
	s.now = func() time.Time { return base.Add(time.Hour + 30*time.Second) }
	if _, err := s.Verify(tok); err != nil {
		t.Fatalf("within skew: %v", err)
	}
	s.now = func() time.Time { return base.Add(time.Hour + 2*time.Minute) }
	if _, err := s.Verify(tok); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestSessionRejections(t *testing.T) {
	s := newTestSessions(t)
	good := s.Issue("s", "a@example.com")
	parts := strings.Split(good, ".")
	hs := map[string]any{"alg": "HS256", "typ": "JWT"}
	other := []byte(strings.Repeat("x", 32))

	with := func(k string, v any) map[string]any {
		c := validClaims()
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
		return c
	}
	cases := map[string]string{
		"not a jwt":         "abc",
		"bad header b64":    "!!!." + parts[1] + "." + parts[2],
		"bad header json":   b64.EncodeToString([]byte("nope")) + "." + parts[1] + "." + parts[2],
		"bad sig b64":       parts[0] + "." + parts[1] + ".!!!",
		"tampered payload":  parts[0] + "." + b64.EncodeToString([]byte(`{"email":"evil@example.com"}`)) + "." + parts[2],
		"unknown key":       forge(other, hs, validClaims()),
		"alg none":          forge(testSecret, map[string]any{"alg": "none"}, validClaims()),
		"alg none unsigned": b64.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + ".",
		"alg HS512":         forge(testSecret, map[string]any{"alg": "HS512"}, validClaims()),
		"alg RS256":         forge(testSecret, map[string]any{"alg": "RS256"}, validClaims()),
		"wrong iss":         forge(testSecret, hs, with("iss", "https://evil")),
		"wrong aud":         forge(testSecret, hs, with("aud", "other")),
		"no email":          forge(testSecret, hs, with("email", nil)),
		"expired":           forge(testSecret, hs, with("exp", time.Now().Add(-time.Hour).Unix())),
		"claims not json":   forgeRaw(testSecret, `{"alg":"HS256"}`, `not json`),
		"claims not b64":    forgeRawPayload(testSecret, `{"alg":"HS256"}`, "!!!"),
	}
	for name, tok := range cases {
		if _, err := s.Verify(tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.VerifySession("abc"); err == nil {
		t.Fatal("VerifySession accepted garbage")
	}
}

func forgeRaw(secret []byte, header, payload string) string {
	signing := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString([]byte(payload))
	return signing + "." + b64.EncodeToString(sign(secret, signing))
}

func forgeRawPayload(secret []byte, header, rawPayload string) string {
	signing := b64.EncodeToString([]byte(header)) + "." + rawPayload
	return signing + "." + b64.EncodeToString(sign(secret, signing))
}

func TestSessionKeyRotation(t *testing.T) {
	oldSecret := []byte(strings.Repeat("o", 32))
	newSecret := []byte(strings.Repeat("n", 40))
	old := newTestSessions(t, oldSecret)
	tok := old.Issue("s", "a@example.com")

	rotated := newTestSessions(t, newSecret, oldSecret)
	if _, err := rotated.Verify(tok); err != nil {
		t.Fatalf("old token after rotation: %v", err)
	}
	fresh := rotated.Issue("s", "a@example.com")
	if _, err := old.Verify(fresh); err == nil {
		t.Fatal("new key must sign new tokens")
	}
	// A token with no kid still verifies against any key.
	if _, err := rotated.Verify(forge(oldSecret, map[string]any{"alg": "HS256"}, validClaims())); err != nil {
		t.Fatalf("kid-less token: %v", err)
	}
}

func TestNewSessionsValidation(t *testing.T) {
	if _, err := NewSessions("x", time.Hour, nil); err == nil {
		t.Fatal("no secret accepted")
	}
	if _, err := NewSessions("x", time.Hour, [][]byte{[]byte("short")}); err == nil {
		t.Fatal("short secret accepted")
	}
}

func TestRefreshCodec(t *testing.T) {
	c, err := NewRefreshCodec([][]byte{testKey})
	if err != nil {
		t.Fatal(err)
	}
	in := refreshPayload{IDPRefreshToken: "rt", Sub: "s", Email: "a@example.com", AuthTime: 42}
	blob := c.seal(in)
	if !strings.HasPrefix(blob, refreshPrefix) || strings.Contains(blob, "rt") && strings.Contains(blob, "a@example.com") {
		t.Fatalf("blob %q", blob)
	}
	out, err := c.open(blob)
	if err != nil || out != in {
		t.Fatalf("round trip: %+v %v", out, err)
	}

	raw, _ := b64.DecodeString(strings.TrimPrefix(blob, refreshPrefix))
	tampered := append([]byte{}, raw...)
	tampered[len(tampered)-1] ^= 1
	unknownKID := append([]byte{}, raw...)
	unknownKID[0] ^= 1
	bad := map[string]string{
		"no prefix":   strings.TrimPrefix(blob, refreshPrefix),
		"wrong ver":   "prt2." + strings.TrimPrefix(blob, refreshPrefix),
		"not b64":     refreshPrefix + "!!!",
		"too short":   refreshPrefix + b64.EncodeToString([]byte{1, 2}),
		"no nonce":    refreshPrefix + b64.EncodeToString(raw[:6]),
		"tampered":    refreshPrefix + b64.EncodeToString(tampered),
		"unknown kid": refreshPrefix + b64.EncodeToString(unknownKID),
		"not json":    sealRaw(t, c, []byte("not json")),
	}
	for name, b := range bad {
		if _, err := c.open(b); err == nil {
			t.Errorf("%s: opened", name)
		}
	}
}

// sealRaw encrypts arbitrary plaintext with the codec's first key.
func sealRaw(t *testing.T, c *RefreshCodec, plain []byte) string {
	t.Helper()
	k := c.keys[0]
	nonce := make([]byte, k.aead.NonceSize())
	out := append(append(k.id[:], nonce...), k.aead.Seal(nil, nonce, plain, k.id[:])...)
	return refreshPrefix + b64.EncodeToString(out)
}

func TestRefreshCodecRotation(t *testing.T) {
	oldKey := []byte(strings.Repeat("o", 32))
	newKey := []byte(strings.Repeat("n", 32))
	old, _ := NewRefreshCodec([][]byte{oldKey})
	blob := old.seal(refreshPayload{Sub: "s"})
	rotated, _ := NewRefreshCodec([][]byte{newKey, oldKey})
	if p, err := rotated.open(blob); err != nil || p.Sub != "s" {
		t.Fatalf("old blob after rotation: %v", err)
	}
	fresh := rotated.seal(refreshPayload{Sub: "s"})
	if _, err := old.open(fresh); err == nil {
		t.Fatal("new key must encrypt new blobs")
	}
}

func TestNewRefreshCodecValidation(t *testing.T) {
	if _, err := NewRefreshCodec(nil); err == nil {
		t.Fatal("no key accepted")
	}
	if _, err := NewRefreshCodec([][]byte{[]byte("short")}); err == nil {
		t.Fatal("short key accepted")
	}
}
