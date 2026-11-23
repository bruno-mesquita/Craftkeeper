package servers

import (
	"context"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// PublicHost returns the host players use to reach the game servers ("" when none is set).
func (s *Service) PublicHost(ctx context.Context) (string, error) {
	return s.cfg.PublicHost(ctx)
}

// SetPublicHost stores the public host ("" clears it) and returns it normalized.
func (s *Service) SetPublicHost(ctx context.Context, host string, actor Actor) (string, error) {
	host = domain.NormalizePublicHost(host)
	if err := domain.ValidatePublicHost(host); err != nil {
		return "", err
	}
	err := s.cfg.SetPublicHost(ctx, host)
	s.record(ctx, actor, domain.AuditPublicAddress, "", host, err == nil)
	return host, err
}
