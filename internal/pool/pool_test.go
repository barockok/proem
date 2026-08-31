package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		pool    Pool
		wantErr bool
	}{
		{name: "empty", pool: Pool{}, wantErr: true},
		{name: "missing id", pool: Pool{Members: []Member{{Type: TypeAnthropicOAuth, Cred: CredRef{Env: "X"}, BaseURL: "https://api.anthropic.com"}}}, wantErr: true},
		{name: "dup id", pool: Pool{Members: []Member{{ID: "a", Type: TypeAnthropicOAuth, Cred: CredRef{Env: "X"}, BaseURL: "https://a.com"}, {ID: "a", Type: TypeAnthropicAPI, Cred: CredRef{Env: "Y"}, BaseURL: "https://b.com"}}}, wantErr: true},
		{name: "invalid type", pool: Pool{Members: []Member{{ID: "a", Type: "bad", Cred: CredRef{Env: "X"}, BaseURL: "https://a.com"}}}, wantErr: true},
		{name: "missing cred", pool: Pool{Members: []Member{{ID: "a", Type: TypeAnthropicOAuth, BaseURL: "https://a.com"}}}, wantErr: true},
		{name: "missing baseURL", pool: Pool{Members: []Member{{ID: "a", Type: TypeAnthropicOAuth, Cred: CredRef{Env: "X"}}}}, wantErr: true},
		{name: "http not https", pool: Pool{Members: []Member{{ID: "a", Type: TypeAnthropicOAuth, Cred: CredRef{Env: "X"}, BaseURL: "http://a.com"}}}, wantErr: true},
		{name: "negative weight", pool: Pool{Members: []Member{{ID: "a", Type: TypeAnthropicOAuth, Cred: CredRef{Env: "X"}, BaseURL: "https://a.com", Weight: -1}}}, wantErr: true},
		{name: "valid", pool: Pool{Members: []Member{{ID: "a", Type: TypeAnthropicOAuth, Cred: CredRef{Env: "X"}, BaseURL: "https://a.com"}, {ID: "b", Type: TypeOpenRouter, Cred: CredRef{File: "/tmp/f"}, BaseURL: "https://b.com", ModelMap: map[string]string{"x": "y"}}}}, wantErr: false},
	}
	for _, tc := range tests {
		err := tc.pool.Validate()
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: got err %v wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	valid := filepath.Join(dir, "valid.yaml")
	if err := os.WriteFile(valid, []byte(`
members:
  - id: a
    type: anthropic_oauth
    cred: {env: CLAUDE_OAT_A}
    baseURL: https://api.anthropic.com
  - id: b
    type: openrouter
    cred: {env: OPENROUTER_KEY}
    baseURL: https://openrouter.ai/api/v1
    modelMap: { "claude-sonnet-4": "anthropic/claude-sonnet-4" }
`), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFile(valid)
	if err != nil {
		t.Fatalf("load valid: %v", err)
	}
	if len(p.Members) != 2 {
		t.Fatalf("want 2 got %d", len(p.Members))
	}
	if p.Members[0].Weight != 1 {
		t.Fatalf("default weight not set")
	}
	// invalid
	invalid := filepath.Join(dir, "invalid.yaml")
	os.WriteFile(invalid, []byte(`members: [{id: a, type: bad, cred: {env: X}, baseURL: https://a.com}]`), 0644)
	if _, err := LoadFile(invalid); err == nil {
		t.Fatal("want err for invalid")
	}
	// missing file
	if _, err := LoadFile(filepath.Join(dir, "no.yaml")); err == nil {
		t.Fatal("want err for missing")
	}
	// bad yaml
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte(`: bad`), 0644)
	if _, err := LoadFile(bad); err == nil {
		t.Fatal("want err for bad yaml")
	}
}

func TestNewLoader(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.yaml")
	os.WriteFile(p, []byte(`
members:
  - id: a
    type: anthropic_oauth
    cred: {env: X}
    baseURL: https://api.anthropic.com
`), 0644)
	l, err := NewLoader(p)
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}
	defer l.Close()
	if l.Active() == nil {
		t.Fatal("active nil")
	}
	if l.Active().Members[0].ID != "a" {
		t.Fatal("wrong id")
	}
	// bad file
	bad := filepath.Join(dir, "bad2.yaml")
	os.WriteFile(bad, []byte(`members: []`), 0644)
	if _, err := NewLoader(bad); err == nil {
		t.Fatal("want err for empty pool")
	}
	if _, err := NewLoader("/no/such/file.yaml"); err == nil {
		t.Fatal("want err for missing")
	}
}

func TestLoaderWeightAndCooldownDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.yaml")
	os.WriteFile(p, []byte(`
members:
  - id: a
    type: anthropic_oauth
    cred: {env: X}
    baseURL: https://api.anthropic.com
    weight: 0
  - id: b
    type: deepseek
    cred: {env: Y}
    baseURL: https://api.deepseek.com
`), 0644)
	l, _ := NewLoader(p)
	defer l.Close()
	act := l.Active()
	if act.Members[0].Weight != 1 {
		t.Fatalf("weight default failed")
	}
	if act.Members[0].CooldownSec != 18000 {
		t.Fatalf("cooldown default anthropic failed got %d", act.Members[0].CooldownSec)
	}
	if act.Members[1].CooldownSec != 60 {
		t.Fatalf("cooldown default deepseek failed got %d", act.Members[1].CooldownSec)
	}
}

func TestLoaderWatchAndClose(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "pool.yaml")
	os.WriteFile(p, []byte(`
members:
  - id: a
    type: anthropic_oauth
    cred: {env: X}
    baseURL: https://api.anthropic.com
`), 0644)
	l, err := NewLoader(p)
	if err != nil {
		t.Fatalf("new loader: %v", err)
	}
	defer l.Close()
	done := make(chan struct{})
	reloadErrs := make(chan error, 8)
	l.Watch(done, func(e error) {
		select {
		case reloadErrs <- e:
		default:
		}
	})
	// trigger reload by writing valid change
	os.WriteFile(p, []byte(`
members:
  - id: b
    type: anthropic_oauth
    cred: {env: Y}
    baseURL: https://api.anthropic.com
`), 0644)
	// poll
	for i := 0; i < 20; i++ {
		if l.Active().Members[0].ID == "b" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if l.Active().Members[0].ID != "b" {
		t.Fatalf("watch did not reload, got %s", l.Active().Members[0].ID)
	}
	// write invalid yaml -> should not swap, onError called
	os.WriteFile(p, []byte(`: bad yaml [`), 0644)
	select {
	case err := <-reloadErrs:
		if err == nil {
			t.Fatal("expected a non-nil reload error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("invalid reload never reported an error")
	}
	if l.Active().Members[0].ID != "b" {
		t.Fatalf("invalid reload swapped pool")
	}
	close(done)
	time.Sleep(50 * time.Millisecond)
	// cover nil watcher path
	empty := &Loader{}
	empty.Watch(nil, nil)
	if err := empty.Close(); err != nil {
		t.Fatalf("close empty: %v", err)
	}
}
