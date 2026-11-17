// Package ttlcache shares one result of a slow collection among callers for a fixed time.
package ttlcache

import (
	"context"
	"sync"
	"time"
)

// Cache serializes collections and reuses a successful result for ttl.
// Errors are not cached, so recovery is visible on the next call.
type Cache[T any] struct {
	ttl     time.Duration
	collect func(context.Context) (T, error)
	now     func() time.Time

	mu     sync.Mutex
	cached T
	at     time.Time
}

func New[T any](ttl time.Duration, collect func(context.Context) (T, error)) *Cache[T] {
	return &Cache[T]{ttl: ttl, collect: collect, now: time.Now}
}

func (c *Cache[T]) Get(ctx context.Context) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && c.now().Sub(c.at) < c.ttl {
		return c.cached, nil
	}
	v, err := c.collect(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	c.cached, c.at = v, c.now()
	return v, nil
}
