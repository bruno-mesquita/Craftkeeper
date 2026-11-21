// Package docker talks to the Docker Engine API over its Unix socket with plain net/http.
//
// Only allowlisted operations are exposed, each as its own method: connectivity checks,
// listing and statistics of containers created by the panel (stats.go), and the
// game server lifecycle (runtime.go). There is no generic passthrough to the Engine API.
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// apiVersion pins the Engine API used for versioned endpoints. 1.44 (Docker Engine 25+)
// supports one-shot stats (added in 1.41); the host runs Engine 29.10 (API 1.57, minimum 1.40).
const apiVersion = "/v1.44"

// defaultTimeout bounds every call that does not ask for a longer one (stop, pull).
const defaultTimeout = 5 * time.Second

// maxErrorBytes bounds the error body read from the daemon.
const maxErrorBytes = 4 << 10

// errTooLarge reports a daemon response above the caller's size limit.
var errTooLarge = errors.New("response too large")

// apiError is a non-2xx answer from the daemon.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string { return fmt.Sprintf("docker: status %d: %s", e.status, e.message) }

type Client struct {
	http *http.Client
}

// NewClient has no client-wide timeout: each call sets its own deadline, because stopping a
// game server or pulling an image legitimately takes far longer than a listing.
func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	return &Client{http: &http.Client{
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
	ctx, cancel := context.WithTimeout(ctx, defaultTimeout)
	defer cancel()
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
	_, err := c.call(ctx, http.MethodGet, pathAndQuery, nil, defaultTimeout, maxBytes, v)
	return err
}

// call sends one request to a versioned Engine API path with the given deadline. in, when
// not nil, is sent as JSON; a 2xx body is decoded into out (when not nil, at most maxBytes).
// A non-2xx status becomes *apiError, except 304 Not Modified, which Docker uses for
// "already started/stopped" and is returned as a status with a nil error.
// A transport failure wraps domain.ErrDockerUnavailable.
func (c *Client) call(ctx context.Context, method, pathAndQuery string, in any, timeout time.Duration, maxBytes int64, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+apiVersion+pathAndQuery, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", domain.ErrDockerUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotModified {
		return resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, readAPIError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBytes))
		return resp.StatusCode, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return resp.StatusCode, err
	}
	if int64(len(b)) > maxBytes {
		return resp.StatusCode, errTooLarge
	}
	return resp.StatusCode, json.Unmarshal(b, out)
}

// stream sends one request to a versioned Engine API path and returns the open response for
// the caller to consume and close. It has no deadline of its own: the stream lives until ctx
// is cancelled or the daemon ends it. Errors are translated like call's.
func (c *Client) stream(ctx context.Context, method, pathAndQuery string, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+apiVersion+pathAndQuery, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: %v", domain.ErrDockerUnavailable, err)
	}
	if (resp.StatusCode < 200 || resp.StatusCode > 299) && resp.StatusCode != http.StatusSwitchingProtocols {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	return &apiError{status: resp.StatusCode, message: msg}
}
