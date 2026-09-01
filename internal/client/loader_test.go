package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "clients.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func registryYAML(name, token string) string {
	return "clients:\n  - name: " + name + "\n    tokenSHA256: " + HashToken(token) + "\n"
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, registryYAML("agent-maria", "tok"))
	reg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := reg.Lookup("tok"); !ok || got.Name != "agent-maria" {
		t.Fatalf("lookup after load: %v %v", got, ok)
	}
}

func TestLoadFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFile(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Fatal("want error for missing file")
	}
	if _, err := LoadFile(writeFile(t, dir, ": not yaml [")); err == nil {
		t.Fatal("want error for malformed yaml")
	}
	if _, err := LoadFile(writeFile(t, t.TempDir(), "clients: []")); err == nil {
		t.Fatal("want error for empty registry")
	}
	if _, err := LoadFile(writeFile(t, t.TempDir(), "clients:\n  - name: a\n    tokenSHA256: nope\n")); err == nil {
		t.Fatal("want error for invalid digest")
	}
}

func TestNewLoader(t *testing.T) {
	path := writeFile(t, t.TempDir(), registryYAML("agent-maria", "tok"))
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if l.Active() == nil {
		t.Fatal("active registry is nil")
	}
	if _, ok := l.Active().Lookup("tok"); !ok {
		t.Fatal("token not resolvable after load")
	}
}

func TestNewLoaderRejectsBadInput(t *testing.T) {
	if _, err := NewLoader(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("want error for missing file")
	}
	if _, err := NewLoader(writeFile(t, t.TempDir(), "clients: []")); err == nil {
		t.Fatal("want error for empty registry")
	}
	if _, err := NewLoader(t.TempDir()); err == nil {
		t.Fatal("want error when path is a directory")
	}
}

func TestWatchAppliesValidReload(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, registryYAML("agent-maria", "tok"))
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, nil)

	writeFile(t, dir, registryYAML("agent-sora", "tok2"))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, ok := l.Active().Lookup("tok2"); ok && c.Name == "agent-sora" {
			if _, stale := l.Active().Lookup("tok"); stale {
				t.Fatal("revoked token still resolves after reload")
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("watcher never applied the reload")
}

func TestWatchKeepsRegistryOnInvalidReload(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, registryYAML("agent-maria", "tok"))
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	errCh := make(chan error, 8)
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})

	writeFile(t, dir, "clients:\n  - name: broken\n    tokenSHA256: not-a-digest\n")

	select {
	case e := <-errCh:
		if e == nil {
			t.Fatal("expected a non-nil reload error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("invalid reload was never reported")
	}
	if _, ok := l.Active().Lookup("tok"); !ok {
		t.Fatal("invalid reload must keep the previous registry")
	}
}

func TestWatchStopsWhenWatcherClosed(t *testing.T) {
	path := writeFile(t, t.TempDir(), registryYAML("a", "tok"))
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, nil)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
}

func TestWatchWithoutWatcherIsNoop(t *testing.T) {
	l := &Loader{}
	l.Watch(nil, nil)
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestActiveReturnsLatestPointer(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, registryYAML("a", "tok"))
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	first := l.Active()
	if err := l.reload(); err != nil {
		t.Fatal(err)
	}
	if l.Active() == first {
		t.Fatal("reload should install a new registry pointer")
	}
	if _, ok := l.Active().Lookup("tok"); !ok {
		t.Fatal("token lost across reload")
	}
}

func TestReloadSurfacesInvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, registryYAML("a", "tok"))
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	writeFile(t, dir, "clients: []")
	if err := l.reload(); err == nil {
		t.Fatal("reload of an invalid file must return an error")
	}
	if _, ok := l.Active().Lookup("tok"); !ok {
		t.Fatal("failed reload must leave the previous registry in place")
	}
}

func TestIssueTokenRoundTripsThroughRegistry(t *testing.T) {
	token, digest, err := IssueToken()
	if err != nil {
		t.Fatal(err)
	}
	reg := &Registry{Clients: []Client{{Name: "agent-issued", TokenSHA256: digest}}}
	if err := reg.Validate(); err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Lookup(token)
	if !ok || c.Name != "agent-issued" {
		t.Fatal("issued token must resolve through the registry it was minted for")
	}
}
