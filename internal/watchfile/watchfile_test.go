package watchfile

import (
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestAffects(t *testing.T) {
	path := filepath.Join("cfg", "clients.yaml")
	cases := []struct {
		name string
		ev   fsnotify.Event
		want bool
	}{
		{"write to file", fsnotify.Event{Name: path, Op: fsnotify.Write}, true},
		{"create file", fsnotify.Event{Name: path, Op: fsnotify.Create}, true},
		{"rename file", fsnotify.Event{Name: path, Op: fsnotify.Rename}, true},
		{"remove file", fsnotify.Event{Name: path, Op: fsnotify.Remove}, true},
		{"chmod file", fsnotify.Event{Name: path, Op: fsnotify.Chmod}, false},
		{"unclean name", fsnotify.Event{Name: "cfg/./clients.yaml", Op: fsnotify.Write}, true},
		{"sibling file", fsnotify.Event{Name: filepath.Join("cfg", "pool.yaml"), Op: fsnotify.Write}, false},
		{"configmap swap", fsnotify.Event{Name: filepath.Join("cfg", "..data"), Op: fsnotify.Create}, true},
		{"configmap elsewhere", fsnotify.Event{Name: filepath.Join("other", "..data"), Op: fsnotify.Create}, false},
	}
	for _, tc := range cases {
		if got := Affects(path, tc.ev); got != tc.want {
			t.Errorf("%s: Affects = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestNewWatchesParentDir(t *testing.T) {
	dir := t.TempDir()
	w, err := New(filepath.Join(dir, "clients.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if got := w.WatchList(); len(got) != 1 || got[0] != dir {
		t.Fatalf("watch list = %v, want [%s]", got, dir)
	}
}

func TestNewMissingDir(t *testing.T) {
	if _, err := New(filepath.Join(t.TempDir(), "absent", "clients.yaml")); err == nil {
		t.Fatal("want error when the parent directory does not exist")
	}
}
