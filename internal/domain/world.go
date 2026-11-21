package domain

import "strings"

// OpsFile lists a server's operators.
const OpsFile = "ops.json"

// LevelDatFile marks the root of a Minecraft world.
const LevelDatFile = "level.dat"

// WorldStagingPrefix names the directory an imported world is unpacked into, in the server
// directory. It is a temporary name: backups skip it and startup removes leftovers.
const WorldStagingPrefix = ".craftkeeper-tmp-world-"

// DefaultOpLevel is the permission level the game gives a new operator when
// server.properties does not set op-permission-level.
const DefaultOpLevel = 4

// Operator is an entry of ops.json.
type Operator struct {
	UUID                string `json:"uuid"`
	Name                string `json:"name"`
	Level               int    `json:"level"`
	BypassesPlayerLimit bool   `json:"bypassesPlayerLimit"`
}

// LevelName is the world directory named by server.properties content ("world" by default),
// as a clean path relative to the server directory.
func LevelName(props string) (string, error) {
	name := "world"
	if v, ok := PropertyValue(props, "level-name"); ok && strings.TrimSpace(v) != "" {
		name = strings.TrimSpace(v)
	}
	p, err := CleanServerPath("level-name", name)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", &ValidationError{Field: "level-name", Message: "the world must be a directory inside the server"}
	}
	return p, nil
}

// OpLevel is the permission level the game gives new operators (op-permission-level, 1–4).
func OpLevel(props string) int {
	v, _ := PropertyValue(props, "op-permission-level")
	switch strings.TrimSpace(v) {
	case "1":
		return 1
	case "2":
		return 2
	case "3":
		return 3
	}
	return DefaultOpLevel
}

// ValidateSeed accepts a world seed as typed in server.properties: one line, at most 64
// characters, no control characters. Empty means a random seed.
func ValidateSeed(seed string) error {
	if len(seed) > 64 {
		return &ValidationError{Field: "seed", Message: "the seed is too long"}
	}
	for _, r := range seed {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return &ValidationError{Field: "seed", Message: "the seed must not contain control characters or backslashes"}
		}
	}
	return nil
}
