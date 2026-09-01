package client

import (
	"os"
	"sync/atomic"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Loader holds the active Registry and swaps it atomically when clients.yaml
// changes. An invalid edit is reported and the previous registry is kept.
type Loader struct {
	path     string
	registry atomic.Pointer[Registry]
	watcher  *fsnotify.Watcher
}

// NewLoader loads clients.yaml and starts watching its directory.
func NewLoader(path string) (*Loader, error) {
	l := &Loader{path: path}
	if err := l.reload(); err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := w.Add(path); err != nil {
		_ = w.Close()
		return nil, err
	}
	l.watcher = w
	return l, nil
}

func (l *Loader) reload() error {
	r, err := LoadFile(l.path)
	if err != nil {
		return err
	}
	l.registry.Store(r)
	return nil
}

// Active returns the current registry.
func (l *Loader) Active() *Registry { return l.registry.Load() }

// Watch reloads on file changes until done is closed, reporting errors to onError.
func (l *Loader) Watch(done <-chan struct{}, onError func(error)) {
	if l.watcher == nil {
		return
	}
	go func() {
		defer l.watcher.Close()
		for {
			select {
			case <-done:
				return
			case ev, ok := <-l.watcher.Events:
				if !ok {
					return
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					if err := l.reload(); err != nil && onError != nil {
						onError(err)
					}
				}
			case err, ok := <-l.watcher.Errors:
				if !ok {
					return
				}
				if onError != nil {
					onError(err)
				}
			}
		}
	}()
}

// Close stops the watcher.
func (l *Loader) Close() error {
	if l.watcher != nil {
		return l.watcher.Close()
	}
	return nil
}

// LoadFile reads and validates a clients.yaml without starting a watcher.
func LoadFile(path string) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Registry
	if err := yaml.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
