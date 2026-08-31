package pool

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const oneMember = `
members:
  - id: a
    type: anthropic_oauth
    cred: {env: X}
    baseURL: https://api.anthropic.com
`

func TestWatchReportsInvalidReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.yaml")
	if err := os.WriteFile(path, []byte(oneMember), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	errCh := make(chan error, 8)
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, func(e error) { errCh <- e })

	// invalid pool: duplicate ids
	bad := `
members:
  - id: dup
    type: anthropic_oauth
    cred: {env: X}
    baseURL: https://api.anthropic.com
  - id: dup
    type: anthropic_oauth
    cred: {env: Y}
    baseURL: https://api.anthropic.com
`
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case e := <-errCh:
		if e == nil {
			t.Fatal("expected non-nil reload error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch never reported the invalid reload")
	}
	if l.Active().Members[0].ID != "a" {
		t.Fatal("invalid reload must keep the previous pool")
	}
}

func TestWatchStopsOnDone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.yaml")
	os.WriteFile(path, []byte(oneMember), 0o644)
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	l.Watch(done, nil)
	close(done)
	time.Sleep(100 * time.Millisecond)
	// watcher goroutine closed the watcher; a second Close must not panic
	_ = l.Close()
}

func TestWatchWithoutWatcherIsNoop(t *testing.T) {
	l := &Loader{}
	l.Watch(nil, nil) // must not panic or start a goroutine
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestNewLoaderDirectoryPath(t *testing.T) {
	if _, err := NewLoader(t.TempDir()); err == nil {
		t.Fatal("want error when path is a directory")
	}
}

func TestWatchExitsWhenWatcherClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.yaml")
	os.WriteFile(path, []byte(oneMember), 0o644)
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, nil)

	// closing the watcher closes its Events/Errors channels; the goroutine
	// must notice and return instead of spinning on closed channels.
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
}

func TestActiveReflectsValidReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.yaml")
	os.WriteFile(path, []byte(oneMember), 0o644)
	l, err := NewLoader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	defer close(done)
	l.Watch(done, nil)

	updated := `
members:
  - id: replaced
    type: openrouter
    cred: {env: OR}
    baseURL: https://openrouter.ai/api/v1
    weight: 4
`
	os.WriteFile(path, []byte(updated), 0o644)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l.Active().Members[0].ID == "replaced" {
			if got := l.Active().Members[0].Weight; got != 4 {
				t.Fatalf("weight not carried through reload: %d", got)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("watcher never picked up the valid reload")
}
