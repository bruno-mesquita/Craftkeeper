package config

import (
	"log/slog"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Listen) != 1 || c.Listen[0] != "127.0.0.1:8090" {
		t.Fatalf("listen = %v", c.Listen)
	}
	if c.GamePorts != (PortRange{First: 25601, Last: 25700}) || c.ServersDir() != "/var/lib/mesquita-panel/servers" {
		t.Fatalf("game ports = %+v, servers dir = %s", c.GamePorts, c.ServersDir())
	}
	if c.DataDir != "/var/lib/mesquita-panel" || c.DockerSocket != "/var/run/docker.sock" || c.LogLevel != slog.LevelInfo {
		t.Fatalf("unexpected defaults %+v", c)
	}
}

func TestMultipleListenAddresses(t *testing.T) {
	c, err := Load(env(map[string]string{"MESQUITA_LISTEN": "127.0.0.1:8090, 100.126.54.17:8090"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Listen) != 2 || c.Listen[1] != "100.126.54.17:8090" {
		t.Fatalf("listen = %v", c.Listen)
	}
}

func TestInvalidValues(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"no port":       {"MESQUITA_LISTEN": "127.0.0.1"},
		"wildcard host": {"MESQUITA_LISTEN": ":8090"},
		"relative dir":  {"MESQUITA_DATA_DIR": "data"},
		"bad level":     {"MESQUITA_LOG_LEVEL": "loud"},
		"ports text":    {"MESQUITA_GAME_PORTS": "many"},
		"ports reverse": {"MESQUITA_GAME_PORTS": "25700-25601"},
		"ports low":     {"MESQUITA_GAME_PORTS": "80-90"},
		"ports high":    {"MESQUITA_GAME_PORTS": "65000-70000"},
		"ports overlap": {"MESQUITA_GAME_PORTS": "8000-9000"},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestGamePorts(t *testing.T) {
	c, err := Load(env(map[string]string{"MESQUITA_GAME_PORTS": " 30000 - 30010 "}))
	if err != nil || c.GamePorts != (PortRange{First: 30000, Last: 30010}) {
		t.Fatalf("range = %+v, %v", c.GamePorts, err)
	}
	c, err = Load(env(map[string]string{"MESQUITA_GAME_PORTS": "30000"}))
	if err != nil || c.GamePorts != (PortRange{First: 30000, Last: 30000}) {
		t.Fatalf("single port = %+v, %v", c.GamePorts, err)
	}
}
