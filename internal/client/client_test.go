package client

import (
	"strings"
	"testing"
)

func enabled(b bool) *bool { return &b }

func validDigest(token string) string { return HashToken(token) }

func TestHashTokenIsStableAndDistinct(t *testing.T) {
	a := HashToken("sk-ant-oat01-aaa")
	if a != HashToken("sk-ant-oat01-aaa") {
		t.Fatal("hash not stable")
	}
	if a == HashToken("sk-ant-oat01-bbb") {
		t.Fatal("distinct tokens collided")
	}
	if len(a) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(a))
	}
}

func TestIssueTokenShape(t *testing.T) {
	token, digest, err := IssueToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) {
		t.Fatalf("issued token must look like an oat, got %q", token)
	}
	if digest != HashToken(token) {
		t.Fatal("digest does not match token")
	}
	other, _, err := IssueToken()
	if err != nil {
		t.Fatal(err)
	}
	if other == token {
		t.Fatal("issued tokens must be unique")
	}
}

func TestValidate(t *testing.T) {
	good := validDigest("t1")
	other := validDigest("t2")

	cases := []struct {
		name    string
		reg     Registry
		wantErr bool
	}{
		{"empty", Registry{}, true},
		{"valid", Registry{Clients: []Client{{Name: "agent-maria", TokenSHA256: good}}}, false},
		{"missing name", Registry{Clients: []Client{{TokenSHA256: good}}}, true},
		{"bad name chars", Registry{Clients: []Client{{Name: "bad name!", TokenSHA256: good}}}, true},
		{"duplicate name", Registry{Clients: []Client{
			{Name: "a", TokenSHA256: good}, {Name: "a", TokenSHA256: other},
		}}, true},
		{"duplicate token", Registry{Clients: []Client{
			{Name: "a", TokenSHA256: good}, {Name: "b", TokenSHA256: good},
		}}, true},
		{"missing digest", Registry{Clients: []Client{{Name: "a"}}}, true},
		{"digest too short", Registry{Clients: []Client{{Name: "a", TokenSHA256: "abc123"}}}, true},
		{"digest not hex", Registry{Clients: []Client{{Name: "a", TokenSHA256: strings.Repeat("z", 64)}}}, true},
		{"uppercase digest accepted", Registry{Clients: []Client{
			{Name: "a", TokenSHA256: strings.ToUpper(good)},
		}}, false},
	}
	for _, tc := range cases {
		reg := tc.reg
		err := reg.Validate()
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", tc.name, err, tc.wantErr)
		}
	}
}

func TestLookup(t *testing.T) {
	reg := Registry{Clients: []Client{
		{Name: "agent-maria", TokenSHA256: validDigest("maria-token")},
		{Name: "agent-sora", TokenSHA256: validDigest("sora-token"), Enabled: enabled(false)},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}

	c, ok := reg.Lookup("maria-token")
	if !ok || c.Name != "agent-maria" {
		t.Fatalf("lookup maria: %v %v", c, ok)
	}
	if !c.IsEnabled() {
		t.Fatal("absent enabled must default to true")
	}

	disabled, ok := reg.Lookup("sora-token")
	if !ok {
		t.Fatal("disabled client should still resolve")
	}
	if disabled.IsEnabled() {
		t.Fatal("explicit enabled:false must be honoured")
	}

	if _, ok := reg.Lookup("not-a-token"); ok {
		t.Fatal("unknown token must not resolve")
	}
	if _, ok := reg.Lookup(""); ok {
		t.Fatal("empty token must not resolve")
	}
}

func TestLookupUppercaseDigestMatches(t *testing.T) {
	reg := Registry{Clients: []Client{
		{Name: "a", TokenSHA256: strings.ToUpper(validDigest("tok"))},
	}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Lookup("tok"); !ok {
		t.Fatal("uppercase digest in config must still match")
	}
}

func TestLookupOnNilAndUnvalidated(t *testing.T) {
	var nilReg *Registry
	if _, ok := nilReg.Lookup("x"); ok {
		t.Fatal("nil registry must not resolve")
	}
	unvalidated := &Registry{Clients: []Client{{Name: "a", TokenSHA256: validDigest("x")}}}
	if _, ok := unvalidated.Lookup("x"); ok {
		t.Fatal("registry without Validate must not resolve")
	}
}

func TestIsEnabled(t *testing.T) {
	if !(Client{}).IsEnabled() {
		t.Fatal("nil enabled means enabled")
	}
	if (Client{Enabled: enabled(false)}).IsEnabled() {
		t.Fatal("false means disabled")
	}
	if !(Client{Enabled: enabled(true)}).IsEnabled() {
		t.Fatal("true means enabled")
	}
}

func TestIssueAndDescribe(t *testing.T) {
	var out strings.Builder
	if err := IssueAndDescribe("agent-maria", &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"agent-maria",
		TokenPrefix,
		"tokenSHA256:",
		"CLAUDE_CODE_OAUTH_TOKEN=",
		"ANTHROPIC_BASE_URL=",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
}

func TestIssueAndDescribeRejectsBadName(t *testing.T) {
	var out strings.Builder
	if err := IssueAndDescribe("not a valid name!", &out); err == nil {
		t.Fatal("invalid client name must be rejected")
	}
	if out.Len() != 0 {
		t.Fatalf("nothing should be printed for a rejected name, got: %s", out.String())
	}
}
