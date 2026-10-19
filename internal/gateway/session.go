package gateway

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Audience is the aud claim of every session JWT proem issues.
const Audience = "proem"

// clockSkew is the leeway allowed on exp when verifying a session JWT.
const clockSkew = 60 * time.Second

// refreshPrefix marks the version of the refresh blob format.
const refreshPrefix = "prt1."

var (
	// ErrInvalidSession is returned for any session JWT that fails verification.
	ErrInvalidSession = errors.New("invalid session")
	// errInvalidRefresh is returned for any refresh blob that cannot be opened.
	errInvalidRefresh = errors.New("invalid refresh token")

	b64 = base64.RawURLEncoding
)

// SessionClaims are the claims of a proem session JWT. The client name is not
// carried: it is resolved from Email on every request, so clients.yaml changes
// take effect immediately.
type SessionClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Subject  string `json:"sub"`
	Email    string `json:"email"`
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`
}

type hmacKey struct {
	id     string
	secret []byte
}

// Sessions issues and verifies HS256 session JWTs. The first key signs; every
// key verifies, so a secret can rotate without logging anyone out.
type Sessions struct {
	issuer string
	ttl    time.Duration
	keys   []hmacKey
	now    func() time.Time
}

// NewSessions builds a Sessions from one or more secrets.
func NewSessions(issuer string, ttl time.Duration, secrets [][]byte) (*Sessions, error) {
	if len(secrets) == 0 {
		return nil, errors.New("session: at least one JWT secret is required")
	}
	s := &Sessions{issuer: issuer, ttl: ttl, now: time.Now}
	for _, secret := range secrets {
		if len(secret) < 32 {
			return nil, errors.New("session: each JWT secret must be at least 32 bytes")
		}
		sum := sha256.Sum256(secret)
		s.keys = append(s.keys, hmacKey{id: hex.EncodeToString(sum[:8]), secret: secret})
	}
	return s, nil
}

// TTL is the lifetime of an issued session JWT.
func (s *Sessions) TTL() time.Duration { return s.ttl }

// Issue signs a session JWT for the given identity.
func (s *Sessions) Issue(sub, email string) string {
	now := s.now()
	claims := SessionClaims{
		Issuer:   s.issuer,
		Audience: Audience,
		Subject:  sub,
		Email:    email,
		IssuedAt: now.Unix(),
		Expiry:   now.Add(s.ttl).Unix(),
	}
	key := s.keys[0]
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT", "kid": key.id})
	payload, _ := json.Marshal(claims)
	signing := b64.EncodeToString(header) + "." + b64.EncodeToString(payload)
	return signing + "." + b64.EncodeToString(sign(key.secret, signing))
}

// VerifySession checks a session JWT and returns the email it was issued
// for. Any failure returns ErrInvalidSession.
func (s *Sessions) VerifySession(token string) (string, error) {
	c, err := s.Verify(token)
	if err != nil {
		return "", err
	}
	return c.Email, nil
}

// Verify checks a session JWT: alg is pinned to HS256 before any key is
// looked up, then the signature, issuer, audience and expiry are checked.
func (s *Sessions) Verify(token string) (SessionClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return SessionClaims{}, ErrInvalidSession
	}
	rawHeader, err := b64.DecodeString(parts[0])
	if err != nil {
		return SessionClaims{}, ErrInvalidSession
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(rawHeader, &header) != nil || header.Alg != "HS256" {
		return SessionClaims{}, ErrInvalidSession
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return SessionClaims{}, ErrInvalidSession
	}
	signing := parts[0] + "." + parts[1]
	if !s.signatureValid(header.Kid, signing, sig) {
		return SessionClaims{}, ErrInvalidSession
	}

	rawClaims, err := b64.DecodeString(parts[1])
	if err != nil {
		return SessionClaims{}, ErrInvalidSession
	}
	var c SessionClaims
	if json.Unmarshal(rawClaims, &c) != nil {
		return SessionClaims{}, ErrInvalidSession
	}
	if c.Issuer != s.issuer || c.Audience != Audience || c.Email == "" {
		return SessionClaims{}, ErrInvalidSession
	}
	if s.now().After(time.Unix(c.Expiry, 0).Add(clockSkew)) {
		return SessionClaims{}, ErrInvalidSession
	}
	return c, nil
}

// signatureValid tries the key named by kid first and then every other key,
// so a token whose kid is missing or stale still verifies during rotation.
func (s *Sessions) signatureValid(kid, signing string, sig []byte) bool {
	for _, k := range s.keys {
		if k.id == kid && hmac.Equal(sign(k.secret, signing), sig) {
			return true
		}
	}
	for _, k := range s.keys {
		if k.id != kid && hmac.Equal(sign(k.secret, signing), sig) {
			return true
		}
	}
	return false
}

func sign(secret []byte, signing string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signing))
	return m.Sum(nil)
}

// refreshPayload is sealed inside the refresh token returned to the client.
type refreshPayload struct {
	IDPRefreshToken string `json:"idp_refresh_token"`
	Sub             string `json:"sub"`
	Email           string `json:"email"`
	// AuthTime is the Unix time of the original SSO login. It is copied
	// unchanged through every refresh and caps the session at session-max.
	AuthTime int64 `json:"auth_time"`
}

type aeadKey struct {
	id   [4]byte
	aead cipher.AEAD
}

// RefreshCodec seals and opens refresh blobs with AES-256-GCM. The first key
// encrypts; every key decrypts.
type RefreshCodec struct {
	keys []aeadKey
}

// NewRefreshCodec builds a codec from one or more 32-byte keys.
func NewRefreshCodec(keys [][]byte) (*RefreshCodec, error) {
	if len(keys) == 0 {
		return nil, errors.New("session: at least one refresh key is required")
	}
	c := &RefreshCodec{}
	for _, k := range keys {
		if len(k) != 32 {
			return nil, errors.New("session: each refresh key must be 32 bytes")
		}
		block, _ := aes.NewCipher(k) // cannot fail for a 32-byte key
		aead, _ := cipher.NewGCM(block)
		sum := sha256.Sum256(k)
		var id [4]byte
		copy(id[:], sum[:4])
		c.keys = append(c.keys, aeadKey{id: id, aead: aead})
	}
	return c, nil
}

// seal encrypts p into an opaque refresh token.
func (c *RefreshCodec) seal(p refreshPayload) string {
	plain, _ := json.Marshal(p)
	k := c.keys[0]
	nonce := make([]byte, k.aead.NonceSize())
	_, _ = rand.Read(nonce)
	out := append(append(k.id[:], nonce...), k.aead.Seal(nil, nonce, plain, k.id[:])...)
	return refreshPrefix + b64.EncodeToString(out)
}

// open decrypts a refresh token. Any failure returns errInvalidRefresh.
func (c *RefreshCodec) open(token string) (refreshPayload, error) {
	rest, ok := strings.CutPrefix(token, refreshPrefix)
	if !ok {
		return refreshPayload{}, errInvalidRefresh
	}
	raw, err := b64.DecodeString(rest)
	if err != nil || len(raw) < 4 {
		return refreshPayload{}, errInvalidRefresh
	}
	for _, k := range c.keys {
		if !hmac.Equal(k.id[:], raw[:4]) {
			continue
		}
		ns := k.aead.NonceSize()
		if len(raw) < 4+ns {
			return refreshPayload{}, errInvalidRefresh
		}
		plain, err := k.aead.Open(nil, raw[4:4+ns], raw[4+ns:], raw[:4])
		if err != nil {
			return refreshPayload{}, errInvalidRefresh
		}
		var p refreshPayload
		if json.Unmarshal(plain, &p) != nil {
			return refreshPayload{}, errInvalidRefresh
		}
		return p, nil
	}
	return refreshPayload{}, errInvalidRefresh
}
