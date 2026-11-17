// Package monitoring serves host resource metrics with a short cache.
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

// Service caches the latest sample for ttl so concurrent dashboards share one collection.
type Service struct {
	collector Collector
	ttl       time.Duration
	now       func() time.Time

	mu     sync.Mutex
	cached domain.SystemStats
	at     time.Time
}

func NewService(c Collector, ttl time.Duration) *Service {
	return &Service{collector: c, ttl: ttl, now: time.Now}
}

func (s *Service) Stats(ctx context.Context) (domain.SystemStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && s.now().Sub(s.at) < s.ttl {
		return s.cached, nil
	}
	st, err := s.collector.Collect(ctx)
	if err != nil {
		return domain.SystemStats{}, err
	}
	s.cached, s.at = st, s.now()
	return st, nil
}
