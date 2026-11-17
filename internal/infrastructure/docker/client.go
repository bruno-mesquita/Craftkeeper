// Package docker talks to the Docker Engine API over its Unix socket with plain net/http.
//
// Only allowlisted operations are exposed. Phase 1 needs connectivity checks only;
// container operations arrive in Phase 3.
package docker

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	http *http.Client
}

func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &Client{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "unix", socketPath)
			},
			MaxIdleConns:    2,
			IdleConnTimeout: 30 * time.Second,
		},
	}}
}

// Ping checks that the daemon is reachable and answering (GET /_ping).
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("docker: ping: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker: ping: unexpected status %d", resp.StatusCode)
	}
	return nil
}
