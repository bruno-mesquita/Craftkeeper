package domain

import "time"

// SystemStats is a point-in-time snapshot of host resource usage.
type SystemStats struct {
	CollectedAt time.Time
	CPU         CPUStats
	Memory      MemoryStats
	Disk        DiskStats
	Network     NetworkStats
}

type CPUStats struct {
	Cores        int
	UsagePercent float64 // 0–100, averaged across all cores since the previous sample
}

type MemoryStats struct {
	TotalBytes     uint64
	UsedBytes      uint64 // total - available
	AvailableBytes uint64
}

// DiskStats describes the filesystem holding the panel's data directory.
type DiskStats struct {
	Path       string
	TotalBytes uint64
	UsedBytes  uint64
	FreeBytes  uint64 // available to unprivileged users
}

// NetworkStats aggregates host interfaces, excluding loopback and container bridges.
type NetworkStats struct {
	RxBytes          uint64
	TxBytes          uint64
	RxBytesPerSecond float64
	TxBytesPerSecond float64
}
