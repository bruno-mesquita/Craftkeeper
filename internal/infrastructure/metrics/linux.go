// Package metrics collects host metrics from Linux /proc and statfs.
package metrics

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

// firstSampleGap is how long the very first Collect waits to compute rates.
const firstSampleGap = 250 * time.Millisecond

// Linux reads metrics from procRoot (normally "/proc"). CPU and network rates are computed
// against the previous Collect call, so no background sampler goroutine is needed.
type Linux struct {
	procRoot string
	diskPath string

	mu      sync.Mutex
	prevCPU cpuTimes
	prevNet netCounters
	prevAt  time.Time
}

func NewLinux(procRoot, diskPath string) *Linux {
	return &Linux{procRoot: procRoot, diskPath: diskPath}
}

func (l *Linux) Collect(ctx context.Context) (domain.SystemStats, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.prevAt.IsZero() {
		cpu, net, err := l.sample()
		if err != nil {
			return domain.SystemStats{}, err
		}
		l.prevCPU, l.prevNet, l.prevAt = cpu, net, time.Now()
		select {
		case <-time.After(firstSampleGap):
		case <-ctx.Done():
			return domain.SystemStats{}, ctx.Err()
		}
	}

	cpu, net, err := l.sample()
	if err != nil {
		return domain.SystemStats{}, err
	}
	now := time.Now()
	elapsed := now.Sub(l.prevAt).Seconds()

	mem, err := l.memory()
	if err != nil {
		return domain.SystemStats{}, err
	}
	disk, err := diskUsage(l.diskPath)
	if err != nil {
		return domain.SystemStats{}, err
	}

	st := domain.SystemStats{
		CollectedAt: now.UTC(),
		CPU:         domain.CPUStats{Cores: runtime.NumCPU(), UsagePercent: cpuPercent(l.prevCPU, cpu)},
		Memory:      mem,
		Disk:        disk,
		Network: domain.NetworkStats{
			RxBytes:          net.rx,
			TxBytes:          net.tx,
			RxBytesPerSecond: rate(l.prevNet.rx, net.rx, elapsed),
			TxBytesPerSecond: rate(l.prevNet.tx, net.tx, elapsed),
		},
	}
	l.prevCPU, l.prevNet, l.prevAt = cpu, net, now
	return st, nil
}

func (l *Linux) sample() (cpuTimes, netCounters, error) {
	cpu, err := readFile(filepath.Join(l.procRoot, "stat"), parseCPU)
	if err != nil {
		return cpuTimes{}, netCounters{}, err
	}
	net, err := readFile(filepath.Join(l.procRoot, "net", "dev"), parseNetDev)
	if err != nil {
		return cpuTimes{}, netCounters{}, err
	}
	return cpu, net, nil
}

func (l *Linux) memory() (domain.MemoryStats, error) {
	return readFile(filepath.Join(l.procRoot, "meminfo"), parseMeminfo)
}

func readFile[T any](path string, parse func(io.Reader) (T, error)) (T, error) {
	f, err := os.Open(path)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("metrics: %w", err)
	}
	defer f.Close()
	v, err := parse(f)
	if err != nil {
		return v, fmt.Errorf("metrics: parse %s: %w", path, err)
	}
	return v, nil
}

type cpuTimes struct{ idle, total uint64 }

// parseCPU reads the aggregate "cpu" line of /proc/stat.
func parseCPU(r io.Reader) (cpuTimes, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var t cpuTimes
		// user nice system idle iowait irq softirq steal (guest columns are already in user/nice).
		for i, f := range fields[1:min(len(fields), 9)] {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			t.total += v
			if i == 3 || i == 4 { // idle + iowait
				t.idle += v
			}
		}
		return t, nil
	}
	if err := sc.Err(); err != nil {
		return cpuTimes{}, err
	}
	return cpuTimes{}, fmt.Errorf("no aggregate cpu line")
}

func cpuPercent(prev, cur cpuTimes) float64 {
	if cur.total <= prev.total {
		return 0
	}
	total := float64(cur.total - prev.total)
	idle := float64(cur.idle - prev.idle)
	return clamp(100*(total-idle)/total, 0, 100)
}

func parseMeminfo(r io.Reader) (domain.MemoryStats, error) {
	var total, avail uint64
	var haveTotal, haveAvail bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			total, haveTotal = v*1024, true
		case "MemAvailable":
			avail, haveAvail = v*1024, true
		}
	}
	if err := sc.Err(); err != nil {
		return domain.MemoryStats{}, err
	}
	if !haveTotal || !haveAvail {
		return domain.MemoryStats{}, fmt.Errorf("MemTotal/MemAvailable missing")
	}
	return domain.MemoryStats{TotalBytes: total, AvailableBytes: avail, UsedBytes: total - min(avail, total)}, nil
}

type netCounters struct{ rx, tx uint64 }

// parseNetDev sums /proc/net/dev counters, skipping loopback and virtual container
// interfaces whose traffic is already counted on the physical/bridge side.
func parseNetDev(r io.Reader) (netCounters, error) {
	var c netCounters
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue // header lines
		}
		name = strings.TrimSpace(name)
		if skipInterface(name) {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			return netCounters{}, fmt.Errorf("interface %s: short line", name)
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return netCounters{}, err
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			return netCounters{}, err
		}
		c.rx += rx
		c.tx += tx
	}
	return c, sc.Err()
}

func skipInterface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

func rate(prev, cur uint64, seconds float64) float64 {
	if seconds <= 0 || cur < prev { // counter reset
		return 0
	}
	return float64(cur-prev) / seconds
}

func diskUsage(path string) (domain.DiskStats, error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return domain.DiskStats{}, fmt.Errorf("metrics: statfs %s: %w", path, err)
	}
	bs := uint64(fs.Bsize)
	total := fs.Blocks * bs
	free := fs.Bfree * bs
	return domain.DiskStats{Path: path, TotalBytes: total, UsedBytes: total - free, FreeBytes: fs.Bavail * bs}, nil
}

func clamp(v, lo, hi float64) float64 { return max(lo, min(v, hi)) }
