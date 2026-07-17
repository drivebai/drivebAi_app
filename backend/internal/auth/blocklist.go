package auth

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// BlockStore is the single read the block list needs. *repository.UserRepository
// satisfies it; tests substitute a fake.
type BlockStore interface {
	IsUserBlocked(ctx context.Context, id uuid.UUID) (bool, error)
}

// BlockChecker answers "is this user blocked?" on every authenticated request
// without a DB round-trip per request: results are cached in-process for a
// short TTL. Enforcement latency is therefore bounded by the TTL on other
// instances and is immediate on the instance that handled the block (BlockUser
// calls Invalidate). The JWT itself stays stateless — no claim changes.
//
// Failure policy: if the store errors (DB blip), the checker FAILS OPEN and
// reports "not blocked" — a blocked user slipping through for one request is
// preferable to locking out every user whenever the DB hiccups. The error is
// logged loudly so it never fails open silently.
type BlockChecker struct {
	store  BlockStore
	ttl    time.Duration
	logger *slog.Logger

	mu      sync.Mutex
	entries map[uuid.UUID]blockEntry
}

type blockEntry struct {
	blocked   bool
	expiresAt time.Time
}

// NewBlockChecker creates a checker with the given cache TTL.
func NewBlockChecker(store BlockStore, ttl time.Duration, logger *slog.Logger) *BlockChecker {
	return &BlockChecker{
		store:   store,
		ttl:     ttl,
		logger:  logger,
		entries: make(map[uuid.UUID]blockEntry),
	}
}

// IsBlocked reports whether the user is blocked, serving from cache when
// fresh. Never returns an error: store failures fail open (see type comment).
func (c *BlockChecker) IsBlocked(ctx context.Context, id uuid.UUID) bool {
	now := time.Now()

	c.mu.Lock()
	if e, ok := c.entries[id]; ok && now.Before(e.expiresAt) {
		c.mu.Unlock()
		return e.blocked
	}
	c.mu.Unlock()

	blocked, err := c.store.IsUserBlocked(ctx, id)
	if err != nil {
		if c.logger != nil {
			c.logger.Error("block check failed — failing open", "user_id", id, "error", err)
		}
		return false
	}

	c.mu.Lock()
	c.entries[id] = blockEntry{blocked: blocked, expiresAt: now.Add(c.ttl)}
	// Opportunistic sweep so the map can't grow unbounded between restarts:
	// evict a handful of stale entries per write instead of running a ticker.
	evicted := 0
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
			if evicted++; evicted >= 16 {
				break
			}
		}
	}
	c.mu.Unlock()

	return blocked
}

// Invalidate drops the cached entry so the next check hits the store. Called
// by the admin block/unblock endpoint for immediate effect in-process.
func (c *BlockChecker) Invalidate(id uuid.UUID) {
	c.mu.Lock()
	delete(c.entries, id)
	c.mu.Unlock()
}
