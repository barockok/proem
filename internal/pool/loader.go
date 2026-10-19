package pool

import (
	"os"
	"sync/atomic"

	"github.com/barockok/proem/internal/watchfile"
	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Loader watches pool.yaml and holds active pool atomically.
type Loader struct {
	path    string
	pool    atomic.Pointer[Pool]
	watcher *fsnotify.Watcher
}

// NewLoader creates loader, loads file immediately.
func NewLoader(path string) (*Loader, error) {
	l := &Loader{path: path}
	if err := l.reload(); err != nil {
		return nil, err
	}
	w, err := watchfile.New(path)
	if err != nil {
		return nil, err
	}
	l.watcher = w
	return l, nil
}

// reload reads and validates file, swaps active pool.
func (l *Loader) reload() error {
	b, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	var p Pool
	if err := yaml.Unmarshal(b, &p); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	// default weight 1 if 0
	for i := range p.Members {
		if p.Members[i].Weight == 0 {
			p.Members[i].Weight = 1
		}
		if p.Members[i].CooldownSec == 0 {
			if p.Members[i].Type == TypeAnthropicOAuth || p.Members[i].Type == TypeAnthropicAPI {
				p.Members[i].CooldownSec = 18000
			} else {
				p.Members[i].CooldownSec = 60
			}
		}
	}
	l.pool.Store(&p)
	return nil
}

// Active returns current pool (never nil after successful NewLoader).
func (l *Loader) Active() *Pool {
	return l.pool.Load()
}

// Watch runs until done channel closed; on file change reloads. Returns reload errors via callback if provided.
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
				if watchfile.Affects(l.path, ev) {
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

// Close stops watcher.
func (l *Loader) Close() error {
	if l.watcher != nil {
		return l.watcher.Close()
	}
	return nil
}

// LoadFile is helper for tests: load and validate without watcher.
func LoadFile(path string) (*Pool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Pool
	if err := yaml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	for i := range p.Members {
		if p.Members[i].Weight == 0 {
			p.Members[i].Weight = 1
		}
		if p.Members[i].CooldownSec == 0 {
			if p.Members[i].Type == TypeAnthropicOAuth || p.Members[i].Type == TypeAnthropicAPI {
				p.Members[i].CooldownSec = 18000
			} else {
				p.Members[i].CooldownSec = 60
			}
		}
	}
	return &p, nil
}
