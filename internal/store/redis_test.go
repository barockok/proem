package store

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestStore(t *testing.T) (*Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil { t.Fatal(err) }
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { c.Close(); mr.Close() })
	return NewWithClient(c), mr
}

func TestCooldown(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	if s.IsCooldown(ctx, "a") { t.Fatal("should not be cool") }
	if err := s.SetCooldown(ctx, "a", 2*time.Second); err != nil { t.Fatal(err) }
	if !s.IsCooldown(ctx, "a") { t.Fatal("should be cool") }
	// filter
	ids := []string{"a","b","c"}
	healthy := s.FilterHealthy(ctx, ids)
	if len(healthy)!=2 || healthy[0]!="b" { t.Fatalf("filter wrong %v", healthy)}
	// nil store fail-open
	var nilStore *Store
	if len(nilStore.FilterHealthy(ctx, ids))!=3 { t.Fatal("nil fail-open") }
	if nilStore.IsCooldown(ctx, "a") { t.Fatal("nil should not cool") }
}

func TestSticky(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestStore(t)
	if _, ok := s.GetSticky(ctx, "sid1"); ok { t.Fatal("should miss") }
	s.SetSticky(ctx, "sid1", "memberA", time.Hour)
	if v, ok := s.GetSticky(ctx, "sid1"); !ok || v!="memberA" { t.Fatalf("sticky got %v %v", v, ok)}
	// empty sid
	if _, ok := s.GetSticky(ctx, ""); ok { t.Fatal("empty sid should miss") }
	if err := s.SetSticky(ctx, "", "x", time.Hour); err != nil { t.Fatalf("empty set %v", err)}
	var nilStore *Store
	if _, ok := nilStore.GetSticky(ctx, "sid"); ok { t.Fatal("nil miss") }
}

func TestStoreNewParseError(t *testing.T) {
	if _, err := New("://bad"); err==nil { t.Fatal("want err") }
}
