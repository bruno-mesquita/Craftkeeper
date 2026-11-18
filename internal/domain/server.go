package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Game identifier. It is persisted and returned by the API.
const GameMinecraft = "minecraft"

// Server is a game server managed by the panel. Its status is not stored: it is derived
// from the container state every time it is read.
type Server struct {
	ID            string
	Name          string
	Description   string
	Game          string
	Loader        string // see Loader* in installer.go
	Version       string // "LATEST" (vanilla only) or a release such as "1.21.4"
	LoaderVersion string // exact loader version; empty for vanilla
	MemoryMB      int
	CPUMillicores int // 1000 = one core
	Port          int // host TCP port of the game
	ContainerID   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ServerStatus string

const (
	StatusRunning    ServerStatus = "running"
	StatusStarting   ServerStatus = "starting" // running, health check not passed yet
	StatusStopped    ServerStatus = "stopped"
	StatusRestarting ServerStatus = "restarting"
	StatusMissing    ServerStatus = "missing" // the container no longer exists
	StatusUnknown    ServerStatus = "unknown" // Docker is unreachable

	// While the panel builds a new server in the background (image pull, directory, container).
	StatusCreating     ServerStatus = "creating"
	StatusCreateFailed ServerStatus = "create_failed" // nothing left but the record; delete it
)

// ContainerState is the runtime state of a managed container.
type ContainerState struct {
	ID       string
	ServerID string
	State    string // Docker state: created, running, restarting, exited, …
	Health   string // Docker health status: starting, healthy, unhealthy, or empty
}

// Status maps a Docker container state to a server status.
func (c ContainerState) Status() ServerStatus {
	switch c.State {
	case "running":
		if c.Health == "starting" {
			return StatusStarting
		}
		return StatusRunning
	case "restarting":
		return StatusRestarting
	default: // created, paused, exited, dead, removing
		return StatusStopped
	}
}

// Server limits.
const (
	ServerNameMaxLen        = 48
	ServerDescriptionMaxLen = 500
	ServerMemoryMinMB       = 512
	ServerMemoryMaxMB       = 16384
	ServerCPUMinMillicores  = 500
)

var minecraftVersion = regexp.MustCompile(`^(LATEST|[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?)$`)

// ValidServerID accepts the IDs the panel generates (crypto/rand.Text: 26 base32 characters),
// so an ID can be used in container names and paths without escaping.
func ValidServerID(id string) bool {
	if len(id) != 26 {
		return false
	}
	for _, r := range id {
		if (r < 'A' || r > 'Z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}

// ValidateServerName accepts 1–48 printable characters without surrounding spaces.
func ValidateServerName(name string) error {
	n := utf8.RuneCountInString(name)
	if n == 0 || n > ServerNameMaxLen || !utf8.ValidString(name) {
		return &ValidationError{Field: "name", Message: "must be 1 to 48 characters"}
	}
	if strings.TrimSpace(name) != name {
		return &ValidationError{Field: "name", Message: "must not start or end with spaces"}
	}
	if strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return &ValidationError{Field: "name", Message: "must not contain control characters"}
	}
	return nil
}

func ValidateServerDescription(desc string) error {
	if !utf8.ValidString(desc) || utf8.RuneCountInString(desc) > ServerDescriptionMaxLen {
		return &ValidationError{Field: "description", Message: "must be at most 500 characters"}
	}
	if strings.ContainsFunc(desc, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) {
		return &ValidationError{Field: "description", Message: "must not contain control characters"}
	}
	return nil
}

// ValidateMinecraftVersion accepts "LATEST" or a release number like "1.21" or "1.21.4".
func ValidateMinecraftVersion(v string) error {
	if !minecraftVersion.MatchString(v) {
		return &ValidationError{Field: "version", Message: `must be "LATEST" or a release such as "1.21.4"`}
	}
	return nil
}

func ValidateServerMemory(mb int) error {
	if mb < ServerMemoryMinMB || mb > ServerMemoryMaxMB {
		return &ValidationError{Field: "memory_mb", Message: "must be between 512 and 16384"}
	}
	return nil
}

// ValidateServerCPU accepts half a core up to every core of the host.
func ValidateServerCPU(millicores, hostCPUs int) error {
	if millicores < ServerCPUMinMillicores || millicores > hostCPUs*1000 {
		return &ValidationError{Field: "cpu_cores", Message: "must be between 0.5 and the number of host CPUs"}
	}
	return nil
}

// ContainerSpec is what the application asks the container runtime to create for a server.
// The runtime adds its own fixed policy (labels, isolation, log limits, restart policy).
type ContainerSpec struct {
	ServerID      string
	Game          string
	Java          string   // one of Java* (installer.go): selects the game image
	Env           []string // KEY=value
	DataDir       string   // absolute host directory mounted as the server's /data
	Port          int      // host TCP port published for the game
	MemoryMB      int
	CPUMillicores int
}

// ServerCounts summarizes servers by status for the dashboard.
type ServerCounts struct {
	Total   int
	Running int // running, starting or restarting
	Stopped int
}
