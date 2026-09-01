// Package client models the callers of the proxy. Each client is issued its
// own Anthropic-shaped token; the proxy uses it for authentication and as the
// attribution label on usage metrics.
package client

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// UnknownClient labels usage that could not be attributed to a named client.
const UnknownClient = "unknown"

var (
	// hashPattern matches a lowercase hex-encoded SHA-256 digest.
	hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// namePattern keeps names safe to use as Prometheus label values.
	namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)
)

// Client is one caller of the proxy.
type Client struct {
	Name        string `yaml:"name" json:"name"`
	TokenSHA256 string `yaml:"tokenSHA256" json:"tokenSHA256"`
	Enabled     *bool  `yaml:"enabled" json:"enabled"`
}

// IsEnabled reports whether the client may call the proxy. Absent means enabled.
func (c Client) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

// Registry is the set of clients loaded from clients.yaml.
type Registry struct {
	Clients []Client `yaml:"clients" json:"clients"`

	// byHash indexes clients by token digest; built by Validate.
	byHash map[string]Client
}

// Validate checks registry invariants and builds the lookup index.
func (r *Registry) Validate() error {
	if len(r.Clients) == 0 {
		return errors.New("clients: no clients defined")
	}
	names := make(map[string]bool, len(r.Clients))
	byHash := make(map[string]Client, len(r.Clients))
	for i, c := range r.Clients {
		if c.Name == "" {
			return fmt.Errorf("clients[%d]: missing name", i)
		}
		if !namePattern.MatchString(c.Name) {
			return fmt.Errorf("clients[%d] %q: name must be alphanumeric with . _ - (max 64 chars)", i, c.Name)
		}
		if names[c.Name] {
			return fmt.Errorf("clients: duplicate name %q", c.Name)
		}
		names[c.Name] = true

		digest := strings.ToLower(c.TokenSHA256)
		if digest == "" {
			return fmt.Errorf("clients[%d] %q: tokenSHA256 required", i, c.Name)
		}
		if !hashPattern.MatchString(digest) {
			return fmt.Errorf("clients[%d] %q: tokenSHA256 must be a hex-encoded SHA-256 digest", i, c.Name)
		}
		if prev, dup := byHash[digest]; dup {
			return fmt.Errorf("clients: %q and %q share the same token", prev.Name, c.Name)
		}
		c.TokenSHA256 = digest
		byHash[digest] = c
	}
	r.byHash = byHash
	return nil
}

// Lookup resolves a presented token to its client. The token is hashed and
// matched against the index, so raw tokens are never held in memory or config.
func (r *Registry) Lookup(token string) (Client, bool) {
	if r == nil || token == "" || r.byHash == nil {
		return Client{}, false
	}
	c, ok := r.byHash[HashToken(token)]
	return c, ok
}

// HashToken returns the lowercase hex SHA-256 digest of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
