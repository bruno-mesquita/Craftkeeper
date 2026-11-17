package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

var (
	idRunning = strings.Repeat("a", 64)
	idStopped = strings.Repeat("b", 64)
	idForeign = strings.Repeat("c", 64)
)

// fakeDaemon serves a minimal Docker Engine API on a unix socket.
type fakeDaemon struct {
	mu        sync.Mutex
	list      string
	stats     map[string]string // container ID -> stats JSON
	listQuery string
	paths     []string
	status    int
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch {
	case r.URL.Path == "/v1.44/containers/json":
		f.listQuery = r.URL.RawQuery
		io.WriteString(w, f.list)
	case strings.HasPrefix(r.URL.Path, "/v1.44/containers/") && strings.HasSuffix(r.URL.Path, "/stats"):
		if r.URL.Query().Get("stream") != "false" || r.URL.Query().Get("one-shot") != "true" {
			http.Error(w, "streaming stats not expected", http.StatusBadRequest)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1.44/containers/"), "/stats")
		s, ok := f.stats[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, s)
	default:
		http.NotFound(w, r)
	}
}

// startFake listens on a short socket path (unix paths are limited to ~108 bytes).
func startFake(t *testing.T, f *fakeDaemon) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(f)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func statsJSON(cpu, system, rx, tx, usage, inactive uint64) string {
	b, _ := json.Marshal(map[string]any{
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": cpu},
			"system_cpu_usage": system,
			"online_cpus":      4,
		},
		"memory_stats": map[string]any{"usage": usage, "limit": 1000, "stats": map[string]any{"inactive_file": inactive}},
		"networks": map[string]any{
			"eth0": map[string]any{"rx_bytes": rx, "tx_bytes": tx},
			"eth1": map[string]any{"rx_bytes": 0, "tx_bytes": 0},
		},
		"blkio_stats": map[string]any{"io_service_bytes_recursive": []any{
			map[string]any{"op": "read", "value": 7},
			map[string]any{"op": "write", "value": 9},
		}},
	})
	return string(b)
}

func listJSON(entries ...map[string]any) string {
	b, _ := json.Marshal(entries)
	return string(b)
}

func entry(id, name, state string, labels map[string]string) map[string]any {
	return map[string]any{"Id": id, "Names": []string{"/" + name}, "State": state, "Labels": labels}
}

var managed = map[string]string{LabelManaged: "true", LabelServerID: "42"}

func newTestCollector(sock string) (*StatsCollector, *time.Time) {
	c := NewStatsCollector(NewClient(sock), slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Unix(1000, 0)
	c.now = func() time.Time { return now }
	return c, &now
}

func TestCollectContainers(t *testing.T) {
	f := &fakeDaemon{
		list: listJSON(
			entry(idRunning, "mesquita-a", "running", managed),
			entry(idStopped, "mesquita-b", "exited", managed),
			entry(idForeign, "crafty", "running", map[string]string{"other": "x"}), // daemon filter bypassed
		),
		stats: map[string]string{idRunning: statsJSON(1_000, 100_000, 500, 100, 300, 100)},
	}
	sock := startFake(t, f)
	c, now := newTestCollector(sock)
	ctx := context.Background()

	snap, err := c.CollectContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.listQuery == "" || !strings.Contains(f.listQuery, "all=true") ||
		!strings.Contains(f.listQuery, "mesquita.managed%3Dtrue") {
		t.Fatalf("list query = %q, want all=true and label filter", f.listQuery)
	}
	if len(snap.Containers) != 2 {
		t.Fatalf("got %d containers, want 2 managed: %+v", len(snap.Containers), snap.Containers)
	}
	a, b := snap.Containers[0], snap.Containers[1]
	if a.Name != "mesquita-a" || a.ServerID != "42" || a.State != "running" {
		t.Fatalf("running container: %+v", a)
	}
	if a.MemoryUsedBytes != 200 || a.MemoryLimitBytes != 1000 || a.NetRxBytes != 500 || a.NetTxBytes != 100 ||
		a.BlockReadBytes != 7 || a.BlockWriteBytes != 9 {
		t.Fatalf("running container counters: %+v", a)
	}
	if a.CPUPercent != 0 || a.NetRxBytesPerSecond != 0 {
		t.Fatalf("first sample must report zero rates: %+v", a)
	}
	if b.State != "exited" || b.MemoryUsedBytes != 0 {
		t.Fatalf("stopped container: %+v", b)
	}
	for _, p := range f.paths {
		if strings.Contains(p, idStopped) || strings.Contains(p, idForeign) {
			t.Fatalf("unexpected request %s", p)
		}
	}

	// Second sample: 2 s later, 500 of 10 000 system ticks on 4 CPUs = 20%.
	f.mu.Lock()
	f.stats[idRunning] = statsJSON(1_500, 110_000, 2_500, 300, 300, 100)
	f.mu.Unlock()
	*now = now.Add(2 * time.Second)
	snap, err = c.CollectContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a = snap.Containers[0]
	if math.Abs(a.CPUPercent-20) > 1e-9 || a.NetRxBytesPerSecond != 1000 || a.NetTxBytesPerSecond != 100 {
		t.Fatalf("rates: cpu=%v rx/s=%v tx/s=%v", a.CPUPercent, a.NetRxBytesPerSecond, a.NetTxBytesPerSecond)
	}

	// A container that disappears is pruned from the sample map.
	f.mu.Lock()
	f.list = listJSON(entry(idStopped, "mesquita-b", "exited", managed))
	f.mu.Unlock()
	if _, err := c.CollectContainers(ctx); err != nil {
		t.Fatal(err)
	}
	if len(c.prev) != 0 {
		t.Fatalf("prev not pruned: %v", c.prev)
	}
}

func TestCollectContainersStatsFailureKeepsContainer(t *testing.T) {
	f := &fakeDaemon{
		list:  listJSON(entry(idRunning, "mesquita-a", "running", managed)),
		stats: map[string]string{idRunning: `{"pad":"` + strings.Repeat("x", maxStatsBytes) + `"}`},
	}
	c, _ := newTestCollector(startFake(t, f))
	snap, err := c.CollectContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Containers) != 1 || snap.Containers[0].MemoryUsedBytes != 0 {
		t.Fatalf("oversized stats must yield zero metrics, got %+v", snap.Containers)
	}
}

func TestCollectContainersDockerUnavailable(t *testing.T) {
	f := &fakeDaemon{status: http.StatusInternalServerError}
	c, _ := newTestCollector(startFake(t, f))
	if _, err := c.CollectContainers(context.Background()); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Fatalf("daemon error: got %v, want ErrDockerUnavailable", err)
	}

	c, _ = newTestCollector(filepath.Join(t.TempDir(), "missing.sock"))
	if _, err := c.CollectContainers(context.Background()); !errors.Is(err, domain.ErrDockerUnavailable) {
		t.Fatalf("missing socket: got %v, want ErrDockerUnavailable", err)
	}
}

func TestMemoryUsed(t *testing.T) {
	if got := memoryUsed(300, map[string]uint64{"total_inactive_file": 50}); got != 250 {
		t.Fatalf("cgroup v1 = %d, want 250", got)
	}
	if got := memoryUsed(100, map[string]uint64{"inactive_file": 500}); got != 100 {
		t.Fatalf("cache above usage = %d, want 100", got)
	}
}

func TestValidContainerID(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{idRunning, true},
		{strings.Repeat("A", 64), false},
		{"../../_ping", false},
		{strings.Repeat("a", 63) + "/", false},
		{strings.Repeat("a", 12), false},
	} {
		id, want := tc.id, tc.want
		if got := validContainerID(id); got != want {
			t.Errorf("validContainerID(%q) = %v, want %v", id, got, want)
		}
	}
}
