package router

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/barockok/pro-ant/internal/pool"
	"github.com/barockok/pro-ant/internal/store"
	"github.com/redis/go-redis/v9"
)

func testMembers() []pool.Member {
	return []pool.Member{
		{ID:"a", Type:pool.TypeAnthropicOAuth, BaseURL:"https://a.com", Weight:1},
		{ID:"b", Type:pool.TypeAnthropicOAuth, BaseURL:"https://b.com", Weight:1},
		{ID:"c", Type:pool.TypeAnthropicOAuth, BaseURL:"https://c.com", Weight:2},
	}
}

func newRouterWithStore(t *testing.T) (*Router, *miniredis.Miniredis) {
	t.Helper()
	mr, _ := miniredis.Run()
	c := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func(){c.Close(); mr.Close()})
	s := store.NewWithClient(c)
	return New(s), mr
}

func TestPickHealthy(t *testing.T) {
	ctx:=context.Background()
	r, _ := newRouterWithStore(t)
	m := testMembers()
	got, err := r.Pick(ctx, m, "", false)
	if err!=nil||got==nil { t.Fatal(err) }
}

func TestPickCooldownFilter(t *testing.T) {
	ctx:=context.Background()
	r, mr := newRouterWithStore(t)
	_ = mr
	s := r.store
	s.SetCooldown(ctx, "a", time.Hour)
	m := testMembers()
	for i:=0;i<10;i++ {
		got,_:=r.Pick(ctx, m, "", false)
		if got.ID=="a" { t.Fatal("picked cooled") }
	}
}

func TestPickStickyHit(t *testing.T) {
	ctx:=context.Background()
	r, _ := newRouterWithStore(t)
	r.store.SetSticky(ctx, "sess1", "b", time.Hour)
	m:=testMembers()
	got,_:=r.Pick(ctx, m, "sess1", true)
	if got.ID!="b" { t.Fatalf("want b got %s", got.ID)}
	// sticky miss falls through to hash
	got2,_:=r.Pick(ctx, m, "sess2", true)
	if got2==nil { t.Fatal()}
}

func TestPickHashDeterministic(t *testing.T) {
	ctx:=context.Background()
	r,_:=newRouterWithStore(t)
	m:=testMembers()
	a,_:=r.Pick(ctx,m,"same",false)
	b,_:=r.Pick(ctx,m,"same",false)
	if a.ID!=b.ID { t.Fatalf("hash not deterministic %s vs %s", a.ID,b.ID)}
}

func TestPickNoHealthy(t *testing.T) {
	ctx:=context.Background()
	r,_:=newRouterWithStore(t)
	for _,id:=range []string{"a","b","c"} { r.store.SetCooldown(ctx,id,time.Hour)}
	_,err:=r.Pick(ctx,testMembers(),"",false)
	if err==nil { t.Fatal("want err")}
}

func TestPickNoStore(t *testing.T) {
	r:=New(nil)
	got,_:=r.Pick(context.Background(),testMembers(),"sess",true)
	if got==nil { t.Fatal()}
}

func TestPickEmpty(t *testing.T) {
	r:=New(nil)
	if _,err:=r.Pick(context.Background(), nil, "", false); err==nil { t.Fatal()}
}
