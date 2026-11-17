// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the complete runtime configuration of the panel.
type Config struct {
	// Listen holds one or more host:port addresses to bind.
	Listen       []string
	DataDir      string
	DockerSocket string
	LogLevel     slog.Level
	// GamePorts is the inclusive range of host TCP ports given to game servers.
	GamePorts PortRange
}

// PortRange is an inclusive range of TCP ports.
type PortRange struct{ First, Last int }

func (r PortRange) Contains(port int) bool { return port >= r.First && port <= r.Last }

const (
	defaultListen       = "127.0.0.1:8090"
	defaultDataDir      = "/var/lib/mesquita-panel"
	defaultDockerSocket = "/var/run/docker.sock"
	// Crafty already uses 25500–25600 on mesquita-server.
	defaultGamePorts = "25601-25700"
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

	ports, err := parsePortRange(valueOr(getenv("MESQUITA_GAME_PORTS"), defaultGamePorts))
	if err != nil {
		return Config{}, fmt.Errorf("MESQUITA_GAME_PORTS: %w", err)
	}
	cfg.GamePorts = ports
	for _, addr := range cfg.Listen {
		_, p, _ := net.SplitHostPort(addr)
		if port, err := strconv.Atoi(p); err == nil && cfg.GamePorts.Contains(port) {
			return Config{}, fmt.Errorf("MESQUITA_GAME_PORTS: range includes the panel port %d", port)
		}
	}

	if err := cfg.LogLevel.UnmarshalText([]byte(valueOr(getenv("MESQUITA_LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("MESQUITA_LOG_LEVEL: %w", err)
	}
	return cfg, nil
}

// DatabasePath is the SQLite file inside the data directory.
func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, "database.sqlite") }

// ServersDir holds one data directory per game server.
func (c Config) ServersDir() string { return filepath.Join(c.DataDir, "servers") }

// parsePortRange parses "first-last" (or a single port) within 1024–65535.
func parsePortRange(s string) (PortRange, error) {
	a, b, found := strings.Cut(s, "-")
	if !found {
		b = a
	}
	first, err1 := strconv.Atoi(strings.TrimSpace(a))
	last, err2 := strconv.Atoi(strings.TrimSpace(b))
	if err1 != nil || err2 != nil {
		return PortRange{}, fmt.Errorf("invalid range %q (want first-last, e.g. 25601-25700)", s)
	}
	if first < 1024 || last > 65535 || first > last {
		return PortRange{}, fmt.Errorf("invalid range %q (ports must be 1024-65535, first <= last)", s)
	}
	return PortRange{First: first, Last: last}, nil
}

func valueOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return strings.TrimSpace(v)
}
