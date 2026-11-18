// Package activity lists the audit log with filters for the panel's activity views.
package activity

import (
	"context"
	"slices"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

type AuditReader interface {
	List(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEvent, error)
}

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Page is one page of events, newest first. NextBefore is the BeforeID of the next page,
// 0 when there is none.
type Page struct {
	Events     []domain.AuditEvent
	NextBefore int64
}

type Service struct {
	audit AuditReader
}

func NewService(audit AuditReader) *Service { return &Service{audit: audit} }

// List validates f (Limit 0 means DefaultLimit) and returns the matching page.
func (s *Service) List(ctx context.Context, f domain.AuditFilter) (Page, error) {
	if f.Limit == 0 {
		f.Limit = DefaultLimit
	}
	switch {
	case f.Limit < 1 || f.Limit > MaxLimit:
		return Page{}, &domain.ValidationError{Field: "limit", Message: "must be between 1 and 200"}
	case f.ServerID != "" && !domain.ValidServerID(f.ServerID):
		return Page{}, &domain.ValidationError{Field: "server_id", Message: "is not a valid server id"}
	case f.Action != "" && !slices.Contains(domain.AuditActions, f.Action):
		return Page{}, &domain.ValidationError{Field: "action", Message: "is not a known action"}
	case f.BeforeID < 0:
		return Page{}, &domain.ValidationError{Field: "before", Message: "must be a positive id"}
	}
	events, err := s.audit.List(ctx, f)
	if err != nil {
		return Page{}, err
	}
	p := Page{Events: events}
	if len(events) == f.Limit {
		p.NextBefore = events[len(events)-1].ID
	}
	return p, nil
}
