package monitoring

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTTLCache(t *testing.T) {
	calls := 0
	fail := false
	c := newTTLCache(2*time.Second, func(context.Context) (int, error) {
		if fail {
			return 0, errors.New("boom")
		}
		calls++
		return calls, nil
	})
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	ctx := context.Background()

	if v, _ := c.get(ctx); v != 1 {
		t.Fatalf("first get = %d, want 1", v)
	}
	now = now.Add(time.Second)
	if v, _ := c.get(ctx); v != 1 {
		t.Fatalf("within ttl = %d, want cached 1", v)
	}
	now = now.Add(2 * time.Second)
	if v, _ := c.get(ctx); v != 2 {
		t.Fatalf("after ttl = %d, want 2", v)
	}

	now = now.Add(3 * time.Second)
	fail = true
	if _, err := c.get(ctx); err == nil {
		t.Fatal("expected error after ttl expired")
	}
	fail = false
	if v, _ := c.get(ctx); v != 3 {
		t.Fatalf("errors must not be cached: got %d, want 3", v)
	}
}
