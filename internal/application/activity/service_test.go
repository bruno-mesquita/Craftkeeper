package activity

import (
	"context"
	"errors"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

type fakeReader struct {
	got    domain.AuditFilter
	events []domain.AuditEvent
}

func (f *fakeReader) List(_ context.Context, filter domain.AuditFilter) ([]domain.AuditEvent, error) {
	f.got = filter
	if len(f.events) > filter.Limit {
		return f.events[:filter.Limit], nil
	}
	return f.events, nil
}

func TestListValidates(t *testing.T) {
	r := &fakeReader{}
	s := NewService(r)
	for field, f := range map[string]domain.AuditFilter{
		"limit":     {Limit: MaxLimit + 1},
		"server_id": {ServerID: "../etc"},
		"action":    {Action: "server.explode"},
		"before":    {BeforeID: -1},
	} {
		var ve *domain.ValidationError
		if _, err := s.List(context.Background(), f); !errors.As(err, &ve) || ve.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	if _, err := s.List(context.Background(), domain.AuditFilter{Limit: -1}); err == nil {
		t.Error("negative limit accepted")
	}

	if _, err := s.List(context.Background(), domain.AuditFilter{Action: domain.AuditConsoleCommand,
		ServerID: "ABCDEFGHIJKLMNOPQRSTUVWXYZ"}); err != nil {
		t.Fatal(err)
	}
	if r.got.Limit != DefaultLimit || r.got.Action != domain.AuditConsoleCommand {
		t.Fatalf("filter passed = %+v", r.got)
	}
}

func TestListNextBefore(t *testing.T) {
	r := &fakeReader{events: []domain.AuditEvent{{ID: 9}, {ID: 7}, {ID: 4}}}
	s := NewService(r)
	p, err := s.List(context.Background(), domain.AuditFilter{Limit: 2})
	if err != nil || len(p.Events) != 2 || p.NextBefore != 7 {
		t.Fatalf("full page = %+v, %v", p, err)
	}
	p, err = s.List(context.Background(), domain.AuditFilter{Limit: 5})
	if err != nil || len(p.Events) != 3 || p.NextBefore != 0 {
		t.Fatalf("last page = %+v, %v", p, err)
	}
}
