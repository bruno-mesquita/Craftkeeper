// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
)

// Config is the complete runtime configuration of the panel.
type Config struct {
	// Listen holds one or more host:port addresses to bind.
	Listen       []string
	DataDir      string
	DockerSocket string
	LogLevel     slog.Level
}

const (
	defaultListen       = "127.0.0.1:8090"
	defaultDataDir      = "/var/lib/mesquita-panel"
	defaultDockerSocket = "/var/run/docker.sock"
)

// Load reads configuration through getenv (os.Getenv in production) and validates it.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		DataDir:      valueOr(getenv("MESQUITA_DATA_DIR"), defaultDataDir),
		DockerSocket: valueOr(getenv("MESQUITA_DOCKER_SOCKET"), defaultDockerSocket),
	}

	for _, addr := range strings.Split(valueOr(getenv("MESQUITA_LISTEN"), defaultListen), ",") {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		host, port, err := net.SplitHostPort(addr)
		if err != nil || port == "" {
			return Config{}, fmt.Errorf("MESQUITA_LISTEN: invalid address %q (want host:port)", addr)
		}
		if host == "" {
			return Config{}, fmt.Errorf("MESQUITA_LISTEN: address %q must name an explicit interface IP", addr)
		}
		cfg.Listen = append(cfg.Listen, addr)
	}
	if len(cfg.Listen) == 0 {
		return Config{}, fmt.Errorf("MESQUITA_LISTEN: no address configured")
	}

	if !filepath.IsAbs(cfg.DataDir) {
		return Config{}, fmt.Errorf("MESQUITA_DATA_DIR: must be an absolute path, got %q", cfg.DataDir)
	}
	if !filepath.IsAbs(cfg.DockerSocket) {
		return Config{}, fmt.Errorf("MESQUITA_DOCKER_SOCKET: must be an absolute path, got %q", cfg.DockerSocket)
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(getenv("MESQUITA_LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("MESQUITA_LOG_LEVEL: %w", err)
	}
	return cfg, nil
}

// DatabasePath is the SQLite file inside the data directory.
func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, "database.sqlite") }

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
