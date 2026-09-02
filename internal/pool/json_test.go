package pool

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The config loader parses YAML, and YAML 1.2 is a superset of JSON, so a JSON
// document is a valid pool file. That is a supported input format rather than
// an accident of the parser, so it is pinned here: the two spellings of the
// same pool must produce identical results.
func TestJSONAndYAMLPoolsAreEquivalent(t *testing.T) {
	const yamlDoc = `
members:
  - id: anthropic-a
    type: anthropic_oauth
    cred:
      env: CLAUDE_OAT_A
    baseURL: https://api.anthropic.com
    weight: 2
    cooldownSec: 900
  - id: openrouter-1
    type: openrouter
    cred:
      file: /run/secrets/openrouter
    baseURL: https://openrouter.ai/api/v1
    enabled: false
    modelMap:
      claude-sonnet-4: anthropic/claude-sonnet-4
`
	const jsonDoc = `{
  "members": [
    {
      "id": "anthropic-a",
      "type": "anthropic_oauth",
      "cred": { "env": "CLAUDE_OAT_A" },
      "baseURL": "https://api.anthropic.com",
      "weight": 2,
      "cooldownSec": 900
    },
    {
      "id": "openrouter-1",
      "type": "openrouter",
      "cred": { "file": "/run/secrets/openrouter" },
      "baseURL": "https://openrouter.ai/api/v1",
      "enabled": false,
      "modelMap": { "claude-sonnet-4": "anthropic/claude-sonnet-4" }
    }
  ]
}`

	dir := t.TempDir()
	fromYAML := loadDoc(t, filepath.Join(dir, "pool.yaml"), yamlDoc)
	fromJSON := loadDoc(t, filepath.Join(dir, "pool.json"), jsonDoc)

	if !reflect.DeepEqual(fromYAML, fromJSON) {
		t.Fatalf("JSON and YAML pools differ:\n yaml: %+v\n json: %+v", fromYAML, fromJSON)
	}

	// Spot-check that the values actually survived, so an equal-but-empty
	// result cannot pass.
	m := fromJSON.Members[0]
	if m.ID != "anthropic-a" || m.Type != TypeAnthropicOAuth || m.Weight != 2 || m.CooldownSec != 900 {
		t.Fatalf("first member decoded wrong: %+v", m)
	}
	if fromJSON.Members[1].Cred.File != "/run/secrets/openrouter" {
		t.Fatalf("cred.file lost: %+v", fromJSON.Members[1].Cred)
	}
	if fromJSON.Members[1].ModelMap["claude-sonnet-4"] != "anthropic/claude-sonnet-4" {
		t.Fatalf("modelMap lost: %+v", fromJSON.Members[1].ModelMap)
	}
	if fromJSON.Members[1].IsEnabled() {
		t.Fatal("enabled:false must be honoured from JSON")
	}
}

// A JSON pool is validated exactly like a YAML one.
func TestJSONPoolIsValidated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.json")
	if err := os.WriteFile(path, []byte(`{"members":[{"id":"a","type":"nonsense","cred":{"env":"X"},"baseURL":"https://a.com"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("an invalid member type must be rejected regardless of file format")
	}
}

// The watcher keys on the path, not the extension, so a .json pool hot-reloads.
func TestJSONPoolHotReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.json")
	os.WriteFile(path, []byte(`{"members":[{"id":"first","type":"generic","cred":{"env":"X"},"baseURL":"https://a.com"}]}`), 0o644)

	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, nil)

	os.WriteFile(path, []byte(`{"members":[{"id":"second","type":"generic","cred":{"env":"Y"},"baseURL":"https://b.com"}]}`), 0o644)
	if !eventually(func() bool { return l.Active().Members[0].ID == "second" }) {
		t.Fatalf("JSON pool did not hot-reload, still %s", l.Active().Members[0].ID)
	}
}

func loadDoc(t *testing.T, path, body string) *Pool {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", filepath.Base(path), err)
	}
	return p
}
