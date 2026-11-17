package domain

import "time"

// ContainerSnapshot is a point-in-time view of the containers managed by the panel.
type ContainerSnapshot struct {
	CollectedAt time.Time
	Containers  []ContainerStats
}

// ContainerStats describes one managed container. Containers that are not running
// carry their state and zero metrics.
type ContainerStats struct {
	ID       string
	Name     string
	ServerID string // value of the mesquita.server_id label; empty when absent
	State    string // Docker state: created, running, paused, restarting, exited, dead…

	CPUPercent float64 // 100 = one full core; rate since the previous sample

	MemoryUsedBytes  uint64 // usage minus page cache (inactive_file), as `docker stats` reports
	MemoryLimitBytes uint64

	NetRxBytes          uint64
	NetTxBytes          uint64
	NetRxBytesPerSecond float64
	NetTxBytesPerSecond float64

	BlockReadBytes  uint64
	BlockWriteBytes uint64
}
