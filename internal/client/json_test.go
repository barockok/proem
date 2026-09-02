package client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The client registry accepts JSON for the same reason the pool does: YAML 1.2
// is a superset of JSON. Pinned here so it stays a supported format.
func TestJSONAndYAMLRegistriesAreEquivalent(t *testing.T) {
	mariaDigest := HashToken("maria-token")
	soraDigest := HashToken("sora-token")

	yamlDoc := "clients:\n" +
		"  - name: agent-maria\n    tokenSHA256: " + mariaDigest + "\n" +
		"  - name: agent-sora\n    tokenSHA256: " + soraDigest + "\n    enabled: false\n"
	jsonDoc := `{"clients":[` +
		`{"name":"agent-maria","tokenSHA256":"` + mariaDigest + `"},` +
		`{"name":"agent-sora","tokenSHA256":"` + soraDigest + `","enabled":false}]}`

	dir := t.TempDir()
	fromYAML := loadRegistry(t, filepath.Join(dir, "clients.yaml"), yamlDoc)
	fromJSON := loadRegistry(t, filepath.Join(dir, "clients.json"), jsonDoc)

	if !reflect.DeepEqual(fromYAML.Clients, fromJSON.Clients) {
		t.Fatalf("JSON and YAML registries differ:\n yaml: %+v\n json: %+v", fromYAML.Clients, fromJSON.Clients)
	}

	c, ok := fromJSON.Lookup("maria-token")
	if !ok || c.Name != "agent-maria" {
		t.Fatalf("token did not resolve from a JSON registry: %+v %v", c, ok)
	}
	disabled, ok := fromJSON.Lookup("sora-token")
	if !ok || disabled.IsEnabled() {
		t.Fatalf("enabled:false must be honoured from JSON: %+v %v", disabled, ok)
	}
}

func TestJSONRegistryIsValidated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	os.WriteFile(path, []byte(`{"clients":[{"name":"a","tokenSHA256":"not-a-digest"}]}`), 0o644)
	if _, err := LoadFile(path); err == nil {
		t.Fatal("a malformed digest must be rejected regardless of file format")
	}
}

func TestJSONRegistryHotReloads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	os.WriteFile(path, []byte(`{"clients":[{"name":"first","tokenSHA256":"`+HashToken("tok1")+`"}]}`), 0o644)

	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, nil)

	os.WriteFile(path, []byte(`{"clients":[{"name":"second","tokenSHA256":"`+HashToken("tok2")+`"}]}`), 0o644)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := l.Active().Lookup("tok2"); ok && c.Name == "second" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("JSON registry did not hot-reload")
}

func loadRegistry(t *testing.T, path, body string) *Registry {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load %s: %v", filepath.Base(path), err)
	}
	return r
}
