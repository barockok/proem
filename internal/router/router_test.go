package router

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/barockok/proem/internal/pool"
	"github.com/barockok/proem/internal/store"
	"github.com/redis/go-redis/v9"
)

func testMembers() []pool.Member {
	return []pool.Member{
		{ID: "a", Type: pool.TypeAnthropicOAuth, BaseURL: "https://a.com", Weight: 1},
		{ID: "b", Type: pool.TypeAnthropicOAuth, BaseURL: "https://b.com", Weight: 1},
		{ID: "c", Type: pool.TypeAnthropicOAuth, BaseURL: "https://c.com", Weight: 2},
	}
}

func newRouterWithStore(t *testing.T) (*Router, *miniredis.Miniredis) {
	t.Helper()
	mr, _ := miniredis.Run()
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { c.Close(); mr.Close() })
	s := store.NewWithClient(c)
	return New(s), mr
}

func TestPickHealthy(t *testing.T) {
	ctx := context.Background()
	r, _ := newRouterWithStore(t)
	m := testMembers()
	got, err := r.Pick(ctx, m, "", false)
	if err != nil || got == nil {
		t.Fatal(err)
	}
}

func TestPickCooldownFilter(t *testing.T) {
	ctx := context.Background()
	r, mr := newRouterWithStore(t)
	_ = mr
	s := r.store
	s.SetCooldown(ctx, "a", time.Hour)
	m := testMembers()
	for i := 0; i < 10; i++ {
		got, _ := r.Pick(ctx, m, "", false)
		if got.ID == "a" {
			t.Fatal("picked cooled")
		}
	}
}

func TestPickStickyHit(t *testing.T) {
	ctx := context.Background()
	r, _ := newRouterWithStore(t)
	r.store.SetSticky(ctx, "sess1", "b", time.Hour)
	m := testMembers()
	got, _ := r.Pick(ctx, m, "sess1", true)
	if got.ID != "b" {
		t.Fatalf("want b got %s", got.ID)
	}
	// sticky miss falls through to hash
	got2, _ := r.Pick(ctx, m, "sess2", true)
	if got2 == nil {
		t.Fatal()
	}
}

func TestPickHashDeterministic(t *testing.T) {
	ctx := context.Background()
	r, _ := newRouterWithStore(t)
	m := testMembers()
	a, _ := r.Pick(ctx, m, "same", false)
	b, _ := r.Pick(ctx, m, "same", false)
	if a.ID != b.ID {
		t.Fatalf("hash not deterministic %s vs %s", a.ID, b.ID)
	}
}

func TestPickNoHealthy(t *testing.T) {
	ctx := context.Background()
	r, _ := newRouterWithStore(t)
	for _, id := range []string{"a", "b", "c"} {
		r.store.SetCooldown(ctx, id, time.Hour)
	}
	_, err := r.Pick(ctx, testMembers(), "", false)
	if err == nil {
		t.Fatal("want err")
	}
}

func TestPickNoStore(t *testing.T) {
	r := New(nil)
	got, _ := r.Pick(context.Background(), testMembers(), "sess", true)
	if got == nil {
		t.Fatal()
	}
}

func TestPickEmpty(t *testing.T) {
	r := New(nil)
	if _, err := r.Pick(context.Background(), nil, "", false); err == nil {
		t.Fatal()
	}
}

func TestPickDisabled(t *testing.T) {
	ctx := context.Background()
	r := New(nil)
	f := false
	m := []pool.Member{{ID: "a", Type: pool.TypeGeneric, BaseURL: "https://a.com", Enabled: &f, Weight: 1}, {ID: "b", Type: pool.TypeGeneric, BaseURL: "https://b.com", Weight: 1}}
	got, _ := r.Pick(ctx, m, "", false)
	if got.ID != "b" {
		t.Fatalf("disabled filter failed got %s", got.ID)
	}
	// all disabled
	m2 := []pool.Member{{ID: "a", Enabled: &f}, {ID: "b", Enabled: &f}}
	if _, err := r.Pick(ctx, m2, "", false); err == nil {
		t.Fatal("want err all disabled")
	}
}

func TestPickStickyPinnedButCooledFallsThrough(t *testing.T) {
	ctx := context.Background()
	r, _ := newRouterWithStore(t)
	// pin the session to "a", then put "a" in cooldown
	r.store.SetSticky(ctx, "sess", "a", time.Hour)
	r.store.SetCooldown(ctx, "a", time.Hour)

	got, err := r.Pick(ctx, testMembers(), "sess", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "a" {
		t.Fatal("cooled member must not be returned even when pinned")
	}
}

func TestPickRespectsWeights(t *testing.T) {
	ctx := context.Background()
	r := New(nil)
	members := []pool.Member{
		{ID: "light", Type: pool.TypeGeneric, BaseURL: "https://a.com", Weight: 1},
		{ID: "heavy", Type: pool.TypeGeneric, BaseURL: "https://b.com", Weight: 9},
	}
	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		m, err := r.Pick(ctx, members, "", false)
		if err != nil {
			t.Fatal(err)
		}
		counts[m.ID]++
	}
	if counts["heavy"] <= counts["light"] {
		t.Fatalf("weight ignored: %v", counts)
	}
	if counts["light"] == 0 {
		t.Fatalf("weight 1 member never selected: %v", counts)
	}
}

func TestPickZeroWeightsStillSelects(t *testing.T) {
	ctx := context.Background()
	r := New(nil)
	members := []pool.Member{
		{ID: "a", Type: pool.TypeGeneric, BaseURL: "https://a.com"},
		{ID: "b", Type: pool.TypeGeneric, BaseURL: "https://b.com"},
	}
	for i := 0; i < 20; i++ {
		if _, err := r.Pick(ctx, members, "", false); err != nil {
			t.Fatalf("zero weights should still select: %v", err)
		}
	}
	if _, err := r.Pick(ctx, members, "session", false); err != nil {
		t.Fatalf("zero weights with session should still select: %v", err)
	}
}

func TestPickHashSpreadsAcrossMembers(t *testing.T) {
	ctx := context.Background()
	r := New(nil)
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		m, err := r.Pick(ctx, testMembers(), fmt.Sprintf("session-%d", i), false)
		if err != nil {
			t.Fatal(err)
		}
		seen[m.ID] = true
	}
	if len(seen) < 2 {
		t.Fatalf("hash routing collapsed onto one member: %v", seen)
	}
}
