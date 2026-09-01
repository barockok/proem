package router

import (
	"context"
	"hash/crc32"
	"math/rand"

	"github.com/barockok/proem/internal/pool"
	"github.com/barockok/proem/internal/store"
)

type Router struct {
	store *store.Store
}

func New(s *store.Store) *Router { return &Router{store: s} }

// Pick returns next healthy member. If sessionID pinned and healthy, return pinned.
// Otherwise weighted hash (lb) or weighted random. Returns error if none healthy.
func (r *Router) Pick(ctx context.Context, members []pool.Member, sessionID string, trySticky bool) (*pool.Member, error) {
	if len(members) == 0 {
		return nil, ErrNoMember
	}
	// filter disabled first
	var enabled []pool.Member
	for _, m := range members {
		if m.IsEnabled() {
			enabled = append(enabled, m)
		}
	}
	if len(enabled) == 0 {
		return nil, ErrNoHealthy
	}
	// filter healthy (cooldown)
	ids := make([]string, len(enabled))
	for i, m := range enabled {
		ids[i] = m.ID
	}
	healthyIDs := ids
	if r.store != nil {
		healthyIDs = r.store.FilterHealthy(ctx, ids)
	}
	if len(healthyIDs) == 0 {
		return nil, ErrNoHealthy
	}
	// build healthy members slice preserving original order
	healthy := make([]pool.Member, 0, len(healthyIDs))
	set := make(map[string]bool, len(healthyIDs))
	for _, id := range healthyIDs {
		set[id] = true
	}
	for _, m := range enabled {
		if set[m.ID] {
			healthy = append(healthy, m)
		}
	}
	// sticky check: if trySticky and session pinned and pinned still healthy, return it
	if trySticky && sessionID != "" && r.store != nil {
		if pinned, ok := r.store.GetSticky(ctx, sessionID); ok {
			for i := range healthy {
				if healthy[i].ID == pinned {
					return &healthy[i], nil
				}
			}
		}
	}
	// hash mode: deterministic if sessionID present, else weighted random
	if sessionID != "" {
		h := crc32.ChecksumIEEE([]byte(sessionID))
		// weighted selection via hash
		total := 0
		for _, m := range healthy {
			total += m.Weight
		}
		if total == 0 {
			total = len(healthy)
		}
		pick := int(h) % total
		acc := 0
		for i := range healthy {
			w := healthy[i].Weight
			if w == 0 {
				w = 1
			}
			acc += w
			if pick < acc {
				return &healthy[i], nil
			}
		}
		return &healthy[0], nil
	}
	// random weighted
	total := 0
	for _, m := range healthy {
		total += m.Weight
	}
	if total == 0 {
		total = len(healthy)
	}
	pick := rand.Intn(total)
	acc := 0
	for i := range healthy {
		w := healthy[i].Weight
		if w == 0 {
			w = 1
		}
		acc += w
		if pick < acc {
			return &healthy[i], nil
		}
	}
	return &healthy[0], nil
}

var (
	ErrNoMember  = errStr("no pool members")
	ErrNoHealthy = errStr("all members in cooldown")
)

type errStr string

func (e errStr) Error() string { return string(e) }
