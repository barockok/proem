package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	coolPrefix   = "cooldown:"
	stickyPrefix = "sticky:"
)

// Store wraps go-redis for cooldown + sticky.
type Store struct {
	client redis.UniversalClient
}

// New creates Store from redis URL.
func New(redisURL string) (*Store, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return &Store{client: redis.NewClient(opt)}, nil
}

// NewWithClient for tests / injection.
func NewWithClient(c redis.UniversalClient) *Store { return &Store{client: c} }

// IsCooldown returns true if key exists.
func (s *Store) IsCooldown(ctx context.Context, memberID string) bool {
	if s == nil || s.client == nil {
		return false
	}
	n, err := s.client.Exists(ctx, coolPrefix+memberID).Result()
	if err != nil {
		return false
	}
	return n == 1
}

// SetCooldown sets cooldown:{id} with TTL, NX not required (overwrite).
func (s *Store) SetCooldown(ctx context.Context, memberID string, ttl time.Duration) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Set(ctx, coolPrefix+memberID, "1", ttl).Err()
}

// FilterHealthy returns ids that are not cooled (fail-open on error -> all healthy).
func (s *Store) FilterHealthy(ctx context.Context, ids []string) []string {
	if s == nil || s.client == nil || len(ids) == 0 {
		return ids
	}
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = coolPrefix + id
	}
	vals, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return ids
	}
	var out []string
	for i, v := range vals {
		if v == nil {
			out = append(out, ids[i])
		}
	}
	return out
}

// GetSticky returns memberID for session, ok false on miss/error.
func (s *Store) GetSticky(ctx context.Context, sessionID string) (string, bool) {
	if s == nil || s.client == nil || sessionID == "" {
		return "", false
	}
	v, err := s.client.Get(ctx, stickyPrefix+sessionID).Result()
	if err != nil {
		return "", false
	}
	return v, true
}

// SetSticky pins session -> member with TTL.
func (s *Store) SetSticky(ctx context.Context, sessionID, memberID string, ttl time.Duration) error {
	if s == nil || s.client == nil || sessionID == "" {
		return nil
	}
	return s.client.Set(ctx, stickyPrefix+sessionID, memberID, ttl).Err()
}

func (s *Store) Close() error {
	if s != nil && s.client != nil {
		return s.client.Close()
	}
	return nil
}
