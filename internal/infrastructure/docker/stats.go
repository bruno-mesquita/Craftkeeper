package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// Labels that identify containers created by the panel (spec §11).
const (
	LabelManaged  = "mesquita.managed"
	LabelServerID = "mesquita.server_id"
	LabelGame     = "mesquita.game"
)

const (
	maxListBytes      = 1 << 20
	maxStatsBytes     = 64 << 10
	statsConcurrency  = 4
	managedLabelValue = "true"
)

var managedFilter = url.Values{
	"all":     {"true"},
	"filters": {`{"label":["` + LabelManaged + `=` + managedLabelValue + `"]}`},
}.Encode()

// StatsCollector samples resource usage of managed containers. CPU and network rates are
// computed against the previous Collect, like the host collector, so no background
// sampler goroutine is needed and the first sample of a container reports zero rates.
type StatsCollector struct {
	client *Client
	log    *slog.Logger
	now    func() time.Time

	mu   sync.Mutex
	prev map[string]statsSample // keyed by container ID; pruned to the containers last listed
}

func NewStatsCollector(c *Client, log *slog.Logger) *StatsCollector {
	return &StatsCollector{client: c, log: log, now: time.Now, prev: map[string]statsSample{}}
}

type containerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	State  string            `json:"State"`
	Status string            `json:"Status"` // human readable, e.g. "Up 5 minutes (health: starting)"
	Labels map[string]string `json:"Labels"`
}

// listManaged lists every container labelled mesquita.managed=true, running or not.
// The daemon-side filter is never trusted alone: the label and the ID format are re-checked.
// A failure means Docker is unavailable.
func (c *Client) listManaged(ctx context.Context) ([]containerSummary, error) {
	var list []containerSummary
	if err := c.getJSON(ctx, "/containers/json?"+managedFilter, maxListBytes, &list); err != nil {
		return nil, fmt.Errorf("%w: list containers: %v", domain.ErrDockerUnavailable, err)
	}
	out := list[:0]
	for _, x := range list {
		if x.Labels[LabelManaged] == managedLabelValue && validContainerID(x.ID) {
			out = append(out, x)
		}
	}
	return out, nil
}

type statsResponse struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

// statsSample holds the cumulative counters needed to compute rates on the next Collect.
type statsSample struct {
	cpuTotal, systemTotal uint64
	rx, tx                uint64
	at                    time.Time
}

// CollectContainers lists managed containers and samples each running one.
// A failure to list means Docker is unavailable; a failure to sample one container
// is logged and that container is reported with zero metrics.
func (s *StatsCollector) CollectContainers(ctx context.Context) (domain.ContainerSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	list, err := s.client.listManaged(ctx)
	if err != nil {
		return domain.ContainerSnapshot{}, err
	}

	out := make([]domain.ContainerStats, 0, len(list))
	for _, c := range list {
		out = append(out, domain.ContainerStats{
			ID:       c.ID,
			Name:     containerName(c.Names),
			ServerID: c.Labels[LabelServerID],
			State:    c.State,
		})
	}

	samples := make([]*statsSample, len(out))
	sem := make(chan struct{}, statsConcurrency)
	var wg sync.WaitGroup
	for i := range out {
		if out[i].State != "running" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			var raw statsResponse
			if err := s.client.getJSON(ctx, "/containers/"+out[i].ID+"/stats?stream=false&one-shot=true", maxStatsBytes, &raw); err != nil {
				s.log.Warn("docker: container stats failed", "container", out[i].Name, "err", err)
				return
			}
			samples[i] = s.apply(&out[i], raw)
		}()
	}
	wg.Wait()

	next := make(map[string]statsSample, len(out))
	for i, smp := range samples {
		if smp != nil {
			next[out[i].ID] = *smp
		}
	}
	s.prev = next
	return domain.ContainerSnapshot{CollectedAt: s.now().UTC(), Containers: out}, nil
}

// apply fills c from raw and returns the sample to keep for the next rate computation.
// It only reads s.prev, which is not modified until every goroutine has finished.
func (s *StatsCollector) apply(c *domain.ContainerStats, raw statsResponse) *statsSample {
	cur := statsSample{
		cpuTotal:    raw.CPUStats.CPUUsage.TotalUsage,
		systemTotal: raw.CPUStats.SystemUsage,
		at:          s.now(),
	}
	for _, n := range raw.Networks {
		cur.rx += n.RxBytes
		cur.tx += n.TxBytes
	}

	c.MemoryLimitBytes = raw.MemoryStats.Limit
	c.MemoryUsedBytes = memoryUsed(raw.MemoryStats.Usage, raw.MemoryStats.Stats)
	c.NetRxBytes, c.NetTxBytes = cur.rx, cur.tx
	for _, e := range raw.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			c.BlockReadBytes += e.Value
		case "write":
			c.BlockWriteBytes += e.Value
		}
	}

	if prev, ok := s.prev[c.ID]; ok {
		cpus := raw.CPUStats.OnlineCPUs
		if cpus == 0 {
			cpus = len(raw.CPUStats.CPUUsage.PercpuUsage)
		}
		if cpus == 0 {
			cpus = runtime.NumCPU()
		}
		c.CPUPercent = cpuPercent(prev, cur, cpus)
		secs := cur.at.Sub(prev.at).Seconds()
		c.NetRxBytesPerSecond = rate(prev.rx, cur.rx, secs)
		c.NetTxBytesPerSecond = rate(prev.tx, cur.tx, secs)
	}
	return &cur
}

// memoryUsed mirrors `docker stats`: page cache that can be reclaimed is not counted.
// cgroup v2 reports inactive_file; cgroup v1 reports total_inactive_file.
func memoryUsed(usage uint64, stats map[string]uint64) uint64 {
	cache, ok := stats["inactive_file"]
	if !ok {
		cache = stats["total_inactive_file"]
	}
	if cache > usage {
		return usage
	}
	return usage - cache
}

func cpuPercent(prev, cur statsSample, cpus int) float64 {
	if cur.cpuTotal < prev.cpuTotal || cur.systemTotal <= prev.systemTotal {
		return 0 // first sample after a restart, or no time elapsed
	}
	cpu := float64(cur.cpuTotal - prev.cpuTotal)
	sys := float64(cur.systemTotal - prev.systemTotal)
	return max(0, min(cpu/sys*float64(cpus)*100, float64(cpus)*100))
}

func rate(prev, cur uint64, seconds float64) float64 {
	if seconds <= 0 || cur < prev {
		return 0
	}
	return float64(cur-prev) / seconds
}

func containerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// validContainerID accepts Docker's 64-character lowercase hex IDs only, so an ID can be
// placed in a request path without escaping.
func validContainerID(id string) bool {
	if len(id) != 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
