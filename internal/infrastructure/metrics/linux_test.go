package metrics

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const statFixture = `cpu  100 0 100 700 100 0 0 0 0 0
cpu0 50 0 50 350 50 0 0 0 0 0
intr 1 2 3
`

const meminfoFixture = `MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:    6000000 kB
Buffers:          100000 kB
`

const netDevFixture = `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0
  eth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0
tailscale0: 500 5 0 0 0 0 0 0 100 1 0 0 0 0 0 0
vethabc: 7777 1 0 0 0 0 0 0 7777 1 0 0 0 0 0 0
docker0: 8888 1 0 0 0 0 0 0 8888 1 0 0 0 0 0 0
`

func TestParseCPU(t *testing.T) {
	c, err := parseCPU(strings.NewReader(statFixture))
	if err != nil {
		t.Fatal(err)
	}
	if c.total != 1000 || c.idle != 800 {
		t.Fatalf("got %+v, want total=1000 idle=800", c)
	}
	next := cpuTimes{total: 1100, idle: 850}
	if got := cpuPercent(c, next); got != 50 {
		t.Fatalf("cpuPercent = %v, want 50", got)
	}
	if got := cpuPercent(next, c); got != 0 {
		t.Fatalf("cpuPercent on counter regression = %v, want 0", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	m, err := parseMeminfo(strings.NewReader(meminfoFixture))
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalBytes != 16000000*1024 || m.AvailableBytes != 6000000*1024 || m.UsedBytes != 10000000*1024 {
		t.Fatalf("unexpected %+v", m)
	}
	if _, err := parseMeminfo(strings.NewReader("MemTotal: 1 kB\n")); err == nil {
		t.Fatal("expected error when MemAvailable is missing")
	}
}

func TestParseNetDevSkipsVirtualInterfaces(t *testing.T) {
	n, err := parseNetDev(strings.NewReader(netDevFixture))
	if err != nil {
		t.Fatal(err)
	}
	if n.rx != 1500 || n.tx != 2100 {
		t.Fatalf("got %+v, want rx=1500 tx=2100", n)
	}
}

func TestCollectFromFakeProc(t *testing.T) {
	proc := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(proc, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", statFixture)
	write("meminfo", meminfoFixture)
	write("net/dev", netDevFixture)

	st, err := NewLinux(proc, proc).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Memory.TotalBytes == 0 || st.Disk.TotalBytes == 0 || st.Network.RxBytes != 1500 {
		t.Fatalf("unexpected stats %+v", st)
	}
}
