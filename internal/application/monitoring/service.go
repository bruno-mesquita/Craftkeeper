// Package monitoring serves host and container resource metrics with a short cache.
package monitoring

import (
	"context"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/application/ttlcache"
	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
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
	host       *ttlcache.Cache[domain.SystemStats]
	containers *ttlcache.Cache[domain.ContainerSnapshot]
}

func NewService(host Collector, containers ContainerCollector, ttl time.Duration) *Service {
	return &Service{
		host:       ttlcache.New(ttl, host.Collect),
		containers: ttlcache.New(ttl, containers.CollectContainers),
	}
}

func (s *Service) Stats(ctx context.Context) (domain.SystemStats, error) {
	return s.host.Get(ctx)
}

// Containers returns managed container metrics, or domain.ErrDockerUnavailable.
func (s *Service) Containers(ctx context.Context) (domain.ContainerSnapshot, error) {
	return s.containers.Get(ctx)
}
