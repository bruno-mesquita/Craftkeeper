// Package dashboard assembles the overview shown on the panel's home page.
package dashboard

import (
	"context"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type DockerPinger interface {
	Ping(ctx context.Context) error
}

type AuditReader interface {
	Recent(ctx context.Context, limit int) ([]domain.AuditEvent, error)
}

const recentEventsLimit = 10

type Overview struct {
	DockerReachable bool
	RecentEvents    []domain.AuditEvent
}

type Service struct {
	docker DockerPinger
	audit  AuditReader
}

func NewService(docker DockerPinger, audit AuditReader) *Service {
	return &Service{docker: docker, audit: audit}
}

func (s *Service) Overview(ctx context.Context) (Overview, error) {
	events, err := s.audit.Recent(ctx, recentEventsLimit)
	if err != nil {
		return Overview{}, err
	}
	return Overview{DockerReachable: s.docker.Ping(ctx) == nil, RecentEvents: events}, nil
}
