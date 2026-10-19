package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Identity is what proem needs to know about a signed-in user.
type Identity struct {
	Subject       string
	Email         string
	EmailVerified bool
	// HostedDomain is Google's hd claim, empty for other providers.
	HostedDomain string
}

// Provider is the IdP leg of the login: the authorization code flow with PKCE
// and nonce, and the server-side refresh.
type Provider interface {
	// AuthCodeURL is the IdP authorize URL the browser is sent to.
	AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error)
	// Exchange trades an authorization code for a verified identity and the
	// IdP refresh token. The ID token's nonce must equal nonce.
	Exchange(ctx context.Context, code, verifier, nonce string) (Identity, string, error)
	// Refresh uses an IdP refresh token server side. It returns the identity
	// from the new ID token, or from userinfo when none is returned, and the
	// rotated refresh token ("" when the IdP did not rotate it).
	Refresh(ctx context.Context, refreshToken string) (Identity, string, error)
}

var (
	// errIdPRejected means the IdP answered and refused (for example an
	// invalid_grant on refresh). The session must end.
	errIdPRejected = errors.New("rejected by identity provider")
	// errIdPUnavailable means the IdP could not be reached or failed. The
	// caller may retry.
	errIdPUnavailable = errors.New("identity provider unavailable")
)

// OIDCConfig configures the OpenID Connect provider.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	// AuthParams are extra authorize URL parameters, for example Google's
	// access_type=offline and prompt=consent.
	AuthParams map[string]string
	// HTTPClient is used for every call to the IdP; nil means the default.
	HTTPClient *http.Client
}

// oidcProvider implements Provider with coreos/go-oidc and x/oauth2.
// Discovery is lazy and retried until it succeeds, so an IdP outage at
// startup does not stop proem from serving token clients.
type oidcProvider struct {
	cfg OIDCConfig

	mu       sync.Mutex
	provider *oidc.Provider
}

// NewOIDCProvider returns a Provider for cfg. It does not contact the IdP.
func NewOIDCProvider(cfg OIDCConfig) Provider {
	return &oidcProvider{cfg: cfg}
}

func (p *oidcProvider) ctx(ctx context.Context) context.Context {
	if p.cfg.HTTPClient != nil {
		return oidc.ClientContext(ctx, p.cfg.HTTPClient)
	}
	return ctx
}

func (p *oidcProvider) discover(ctx context.Context) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, nil
	}
	// go-oidc keeps the context it was given for later JWKS fetches, so it
	// must outlive this request.
	prov, err := oidc.NewProvider(p.ctx(context.WithoutCancel(ctx)), p.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("%w: discovery: %v", errIdPUnavailable, err)
	}
	p.provider = prov
	return prov, nil
}

func (p *oidcProvider) oauth2Config(prov *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		RedirectURL:  p.cfg.RedirectURL,
		Endpoint:     prov.Endpoint(),
		Scopes:       p.cfg.Scopes,
	}
}

func (p *oidcProvider) AuthCodeURL(ctx context.Context, state, nonce, verifier string) (string, error) {
	prov, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	for k, v := range p.cfg.AuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	return p.oauth2Config(prov).AuthCodeURL(state, opts...), nil
}

func (p *oidcProvider) Exchange(ctx context.Context, code, verifier, nonce string) (Identity, string, error) {
	prov, err := p.discover(ctx)
	if err != nil {
		return Identity{}, "", err
	}
	tok, err := p.oauth2Config(prov).Exchange(p.ctx(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, "", classify(err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return Identity{}, "", fmt.Errorf("%w: no id_token in code exchange", errIdPRejected)
	}
	id, gotNonce, err := p.verifyIDToken(ctx, prov, raw)
	if err != nil {
		return Identity{}, "", err
	}
	if gotNonce != nonce {
		return Identity{}, "", fmt.Errorf("%w: id_token nonce mismatch", errIdPRejected)
	}
	return id, tok.RefreshToken, nil
}

func (p *oidcProvider) Refresh(ctx context.Context, refreshToken string) (Identity, string, error) {
	prov, err := p.discover(ctx)
	if err != nil {
		return Identity{}, "", err
	}
	src := p.oauth2Config(prov).TokenSource(p.ctx(ctx), &oauth2.Token{RefreshToken: refreshToken})
	tok, err := src.Token()
	if err != nil {
		return Identity{}, "", classify(err)
	}
	rotated := ""
	if tok.RefreshToken != refreshToken {
		rotated = tok.RefreshToken
	}
	if raw, ok := tok.Extra("id_token").(string); ok && raw != "" {
		id, _, err := p.verifyIDToken(ctx, prov, raw)
		return id, rotated, err
	}
	info, err := prov.UserInfo(p.ctx(ctx), oauth2.StaticTokenSource(tok))
	if err != nil {
		return Identity{}, "", fmt.Errorf("%w: userinfo: %v", errIdPUnavailable, err)
	}
	var claims rawClaims
	if err := info.Claims(&claims); err != nil {
		return Identity{}, "", fmt.Errorf("%w: userinfo claims: %v", errIdPRejected, err)
	}
	id := claims.identity()
	id.Subject = info.Subject
	return id, rotated, nil
}

// verifyIDToken checks the ID token's signature, issuer, audience and expiry
// and returns its identity and nonce.
func (p *oidcProvider) verifyIDToken(ctx context.Context, prov *oidc.Provider, raw string) (Identity, string, error) {
	tok, err := prov.Verifier(&oidc.Config{ClientID: p.cfg.ClientID}).Verify(p.ctx(ctx), raw)
	if err != nil {
		return Identity{}, "", fmt.Errorf("%w: id_token: %v", errIdPRejected, err)
	}
	var claims rawClaims
	if err := tok.Claims(&claims); err != nil {
		return Identity{}, "", fmt.Errorf("%w: id_token claims: %v", errIdPRejected, err)
	}
	id := claims.identity()
	id.Subject = tok.Subject
	return id, tok.Nonce, nil
}

// rawClaims are the identity claims proem reads. email_verified is a string
// in some IdPs, so it is decoded loosely.
type rawClaims struct {
	Email         string `json:"email"`
	EmailVerified any    `json:"email_verified"`
	HostedDomain  string `json:"hd"`
}

func (c rawClaims) identity() Identity {
	verified := false
	switch v := c.EmailVerified.(type) {
	case bool:
		verified = v
	case string:
		verified = strings.EqualFold(v, "true")
	}
	return Identity{Email: c.Email, EmailVerified: verified, HostedDomain: c.HostedDomain}
}

// classify maps an oauth2 token endpoint failure to rejected (the IdP said
// no) or unavailable (network failure or a server error).
func classify(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.Response != nil && re.Response.StatusCode < 500 {
		return fmt.Errorf("%w: %s", errIdPRejected, re.ErrorCode)
	}
	return fmt.Errorf("%w: %v", errIdPUnavailable, err)
}

// checkIdentity applies proem's claim rules to an identity. It returns a
// metric reason and a message for the user, or "" when the identity passes.
func checkIdentity(id Identity, allowedDomain string) (reason, message string) {
	if id.Subject == "" || id.Email == "" {
		return "sso_missing_email", "Your identity provider did not return an email address."
	}
	if !id.EmailVerified {
		return "sso_email_unverified", "Your email address is not verified with your identity provider."
	}
	if allowedDomain != "" {
		_, domain, _ := strings.Cut(id.Email, "@")
		if !strings.EqualFold(domain, allowedDomain) ||
			(id.HostedDomain != "" && !strings.EqualFold(id.HostedDomain, allowedDomain)) {
			return "sso_wrong_domain", "This account is not in the allowed domain."
		}
	}
	return "", ""
}
