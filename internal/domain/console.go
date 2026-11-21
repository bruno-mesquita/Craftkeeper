package domain

import (
	"time"
	"unicode"
	"unicode/utf8"
)

// Console limits (docs/WEBSOCKET.md).
const (
	ConsoleCommandMaxBytes = 1024
	ConsoleLineMaxBytes    = 4 << 10
)

// ConsoleStream names the container output stream a line came from.
type ConsoleStream string

const (
	StreamStdout ConsoleStream = "stdout"
	StreamStderr ConsoleStream = "stderr"
)

// ConsoleLine is one line of a server's console output, without its line terminator.
type ConsoleLine struct {
	Time   time.Time
	Stream ConsoleStream
	Text   string
}

// ContainerEvent is a lifecycle event of a managed container, as reported by Docker.
type ContainerEvent struct {
	ContainerID string
	ServerID    string // server ID label; may be empty or stale, never trusted alone
	Action      string // start, die, stop, restart, health_status, destroy…
}

// ValidateConsoleCommand accepts one line of printable UTF-8 text: a command cannot smuggle
// a second line or terminal control sequences into the game console.
func ValidateConsoleCommand(cmd string) error {
	switch {
	case cmd == "":
		return &ValidationError{Field: "command", Message: "command is required"}
	case len(cmd) > ConsoleCommandMaxBytes:
		return &ValidationError{Field: "command", Message: "command is too long"}
	case !utf8.ValidString(cmd):
		return &ValidationError{Field: "command", Message: "command must be valid UTF-8"}
	}
	for _, r := range cmd {
		if unicode.IsControl(r) {
			return &ValidationError{Field: "command", Message: "command must not contain control characters"}
		}
	}
	return nil
}
