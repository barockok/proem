// Package watchfile watches a single config file in a way that survives the
// file being replaced rather than written in place.
//
// Watching the file itself breaks on the first atomic replace (editors that
// save via rename, `mv tmp clients.yaml`, Kubernetes ConfigMap updates): the
// watch stays on the old inode and every later change is missed. Watching the
// parent directory and filtering by name keeps working across replacements.
package watchfile

import (
	"path/filepath"

	"github.com/fsnotify/fsnotify"
)

// configMapData is the symlink Kubernetes swaps when a mounted ConfigMap
// changes. The visible file is a symlink through it, so the file's own name
// never sees an event.
const configMapData = "..data"

// New returns a watcher on the directory that holds path.
func New(path string) (*fsnotify.Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(filepath.Dir(filepath.Clean(path))); err != nil {
		_ = w.Close()
		return nil, err
	}
	return w, nil
}

// Affects reports whether ev may have changed the contents of path. Chmod is
// ignored; removals count because a replace can surface as a remove of the
// old file, and a reload that then fails keeps the previous config.
func Affects(path string, ev fsnotify.Event) bool {
	if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
		return false
	}
	path = filepath.Clean(path)
	name := filepath.Clean(ev.Name)
	if name == path {
		return true
	}
	return filepath.Dir(name) == filepath.Dir(path) && filepath.Base(name) == configMapData
}
