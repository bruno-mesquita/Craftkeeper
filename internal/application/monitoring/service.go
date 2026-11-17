// Package monitoring serves host and container resource metrics with a short cache.
package monitoring

import (
	"context"
	"sync"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Collector samples host metrics.
type Collector interface {
	Collect(ctx context.Context) (domain.SystemStats, error)
}

// ContainerCollector samples metrics of the containers managed by the panel.
type ContainerCollector interface {
	CollectContainers(ctx context.Context) (domain.ContainerSnapshot, error)
}

// Service caches the latest samples for ttl so concurrent dashboards share one collection.
type Service struct {
	host       *ttlCache[domain.SystemStats]
	containers *ttlCache[domain.ContainerSnapshot]
}

func NewService(host Collector, containers ContainerCollector, ttl time.Duration) *Service {
	return &Service{
		host:       newTTLCache(ttl, host.Collect),
		containers: newTTLCache(ttl, containers.CollectContainers),
	}
}

func (s *Service) Stats(ctx context.Context) (domain.SystemStats, error) {
	return s.host.get(ctx)
}

// Containers returns managed container metrics, or domain.ErrDockerUnavailable.
func (s *Service) Containers(ctx context.Context) (domain.ContainerSnapshot, error) {
	return s.containers.get(ctx)
}

// ttlCache serializes collections and reuses a successful result for ttl.
// Errors are not cached, so recovery is visible on the next call.
type ttlCache[T any] struct {
	ttl     time.Duration
	collect func(context.Context) (T, error)
	now     func() time.Time

	mu     sync.Mutex
	cached T
	at     time.Time
}

func newTTLCache[T any](ttl time.Duration, collect func(context.Context) (T, error)) *ttlCache[T] {
	return &ttlCache[T]{ttl: ttl, collect: collect, now: time.Now}
}

func (c *ttlCache[T]) get(ctx context.Context) (T, error) {
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
