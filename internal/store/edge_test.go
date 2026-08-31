package store

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// downStore returns a Store whose Redis is unreachable, to exercise fail-open paths.
func downStore(t *testing.T) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	addr := mr.Addr()
	mr.Close() // nothing is listening now
	return NewWithClient(redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 100 * time.Millisecond}))
}

func TestNewValidURL(t *testing.T) {
	s, err := New("redis://localhost:6379/0")
	if err != nil {
		t.Fatalf("valid URL: %v", err)
	}
	if s == nil || s.client == nil {
		t.Fatal("expected client")
	}
	_ = s.Close()
}

func TestRedisDownFailsOpen(t *testing.T) {
	ctx := context.Background()
	s := downStore(t)
	defer s.Close()

	if s.IsCooldown(ctx, "a") {
		t.Fatal("redis down must report not-cooled (fail open)")
	}
	ids := []string{"a", "b"}
	if got := s.FilterHealthy(ctx, ids); len(got) != 2 {
		t.Fatalf("redis down must treat all as healthy, got %v", got)
	}
	if _, ok := s.GetSticky(ctx, "sid"); ok {
		t.Fatal("redis down must report sticky miss")
	}
	if err := s.SetCooldown(ctx, "a", time.Minute); err == nil {
		t.Fatal("SetCooldown should surface the redis error")
	}
}

func TestNilStoreWrites(t *testing.T) {
	ctx := context.Background()
	var s *Store
	if err := s.SetCooldown(ctx, "a", time.Minute); err != nil {
		t.Fatalf("nil SetCooldown: %v", err)
	}
	if err := s.SetSticky(ctx, "sid", "a", time.Minute); err != nil {
		t.Fatalf("nil SetSticky: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

func TestFilterHealthyEmptyInput(t *testing.T) {
	s := NewWithClient(nil)
	if got := s.FilterHealthy(context.Background(), nil); got != nil {
		t.Fatalf("empty input should pass through, got %v", got)
	}
}

func TestCloseReleasesClient(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer mr.Close()
	s := NewWithClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
