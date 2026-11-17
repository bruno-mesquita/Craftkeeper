// Package dashboard assembles the overview shown on the panel's home page.
package dashboard

import (
	"context"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type DockerPinger interface {
	Ping(ctx context.Context) error
}

type ServerCounter interface {
	Counts(ctx context.Context) (domain.ServerCounts, error)
}

type AuditReader interface {
	Recent(ctx context.Context, limit int) ([]domain.AuditEvent, error)
}

const recentEventsLimit = 10

type Overview struct {
	DockerReachable bool
	Servers         domain.ServerCounts
	RecentEvents    []domain.AuditEvent
}

type Service struct {
	docker  DockerPinger
	servers ServerCounter
	audit   AuditReader
}

func NewService(docker DockerPinger, servers ServerCounter, audit AuditReader) *Service {
	return &Service{docker: docker, servers: servers, audit: audit}
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	events, err := s.audit.Recent(ctx, recentEventsLimit)
	if err != nil {
		return Overview{}, err
	}
	counts, err := s.servers.Counts(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{DockerReachable: s.docker.Ping(ctx) == nil, Servers: counts, RecentEvents: events}, nil
}
