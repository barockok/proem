package pool

import (
	"errors"
	"fmt"
	"net/url"
)

// MemberType enumerates upstream kinds.
type MemberType string

const (
	TypeAnthropicOAuth MemberType = "anthropic_oauth"
	TypeAnthropicAPI   MemberType = "anthropic_api"
	TypeOpenRouter     MemberType = "openrouter"
	TypeDeepSeek       MemberType = "deepseek"
	TypeGeneric        MemberType = "generic"
)

var validTypes = map[MemberType]bool{
	TypeAnthropicOAuth: true,
	TypeAnthropicAPI:   true,
	TypeOpenRouter:     true,
	TypeDeepSeek:       true,
	TypeGeneric:        true,
}

// CredRef points to a secret via env or file.
type CredRef struct {
	Env  string `yaml:"env" json:"env"`
	File string `yaml:"file" json:"file"`
}

// Member is one pool entry.
type Member struct {
	ID          string            `yaml:"id" json:"id"`
	Type        MemberType        `yaml:"type" json:"type"`
	Cred        CredRef           `yaml:"cred" json:"cred"`
	BaseURL     string            `yaml:"baseURL" json:"baseURL"`
	ModelMap    map[string]string `yaml:"modelMap" json:"modelMap"`
	Weight      int               `yaml:"weight" json:"weight"`
	Enabled     bool              `yaml:"enabled" json:"enabled"`
	CooldownSec int               `yaml:"cooldownSec" json:"cooldownSec"`
}

// Pool holds all members.
type Pool struct {
	Members []Member `yaml:"members" json:"members"`
}

// Validate checks pool invariants.
func (p *Pool) Validate() error {
	if len(p.Members) == 0 {
		return errors.New("pool: no members")
	}
	seen := make(map[string]bool)
	for i, m := range p.Members {
		if m.ID == "" {
			return fmt.Errorf("pool.members[%d]: missing id", i)
		}
		if seen[m.ID] {
			return fmt.Errorf("pool: duplicate id %q", m.ID)
		}
		seen[m.ID] = true
		if !validTypes[m.Type] {
			return fmt.Errorf("pool.members[%d] %q: invalid type %q", i, m.ID, m.Type)
		}
		if m.Cred.Env == "" && m.Cred.File == "" {
			return fmt.Errorf("pool.members[%d] %q: cred.env or cred.file required", i, m.ID)
		}
		if m.BaseURL == "" {
			return fmt.Errorf("pool.members[%d] %q: baseURL required", i, m.ID)
		}
		u, err := url.Parse(m.BaseURL)
		if err != nil || u.Scheme != "https" {
			return fmt.Errorf("pool.members[%d] %q: baseURL must be https: %q", i, m.ID, m.BaseURL)
		}
		if m.Weight < 0 {
			return fmt.Errorf("pool.members[%d] %q: weight must be >=0", i, m.ID)
		}
	}
	return nil
}
