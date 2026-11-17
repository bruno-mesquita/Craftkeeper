// Package docker talks to the Docker Engine API over its Unix socket with plain net/http.
//
// Only allowlisted, read-only operations are exposed so far: connectivity checks and
// container listing/statistics for containers labelled mesquita.managed=true.
// Container lifecycle operations arrive in Phase 3.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// apiVersion pins the Engine API used for versioned endpoints. 1.44 (Docker Engine 25+)
// supports one-shot stats (added in 1.41); the host runs Engine 29.10 (API 1.57, minimum 1.40).
const apiVersion = "/v1.44"

// errTooLarge reports a daemon response above the caller's size limit.
var errTooLarge = errors.New("response too large")

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

// getJSON GETs a versioned Engine API path and decodes at most maxBytes of JSON into v.
func (c *Client) getJSON(ctx context.Context, pathAndQuery string, maxBytes int64, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+apiVersion+pathAndQuery, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxBytes {
		return errTooLarge
	}
	return json.Unmarshal(body, v)
}
