package domain

import (
	"crypto/md5" //nolint:gosec // not a security use: Minecraft derives offline UUIDs with MD5
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Files of a Minecraft server that hold its player lists and settings.
const (
	WhitelistFile     = "whitelist.json"
	BannedPlayersFile = "banned-players.json"
	BannedIPsFile     = "banned-ips.json"
	PropertiesFile    = "server.properties"
)

// BanReasonMaxLen bounds a ban reason, in characters.
const BanReasonMaxLen = 200

// Player is a whitelist entry.
type Player struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// OnlinePlayers is who a running game reports as connected. Players may list fewer than
// Online: the game shares at most a sample of names.
type OnlinePlayers struct {
	Online  int
	Max     int
	Players []Player
}

// PlayerBan is an entry of banned-players.json.
type PlayerBan struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Created string `json:"created"`
	Source  string `json:"source"`
	Expires string `json:"expires"`
	Reason  string `json:"reason"`
}

// IPBan is an entry of banned-ips.json.
type IPBan struct {
	IP      string `json:"ip"`
	Created string `json:"created"`
	Source  string `json:"source"`
	Expires string `json:"expires"`
	Reason  string `json:"reason"`
}

var playerNameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// ValidatePlayerName accepts a Minecraft Java account name.
func ValidatePlayerName(name string) error {
	if !playerNameRE.MatchString(name) {
		return &ValidationError{Field: "name", Message: "a player name has 1 to 16 letters, digits or underscores"}
	}
	return nil
}

// NormalizeIP validates an IPv4 or IPv6 address and returns its canonical form.
func NormalizeIP(ip string) (string, error) {
	a, err := netip.ParseAddr(ip)
	if err != nil || a.Zone() != "" {
		return "", &ValidationError{Field: "ip", Message: "not a valid IP address"}
	}
	return a.Unmap().String(), nil
}

// ValidateBanReason accepts an optional one-line reason.
func ValidateBanReason(reason string) error {
	if utf8.RuneCountInString(reason) > BanReasonMaxLen || !utf8.ValidString(reason) {
		return &ValidationError{Field: "reason", Message: "the reason is too long"}
	}
	for _, r := range reason {
		if unicode.IsControl(r) {
			return &ValidationError{Field: "reason", Message: "the reason must not contain control characters"}
		}
	}
	return nil
}

// OfflineUUID is the UUID an offline-mode server gives a player: the version 3 UUID of
// "OfflinePlayer:<name>". Online-mode servers use the Mojang account UUID instead.
func OfflineUUID(name string) string {
	h := md5.Sum([]byte("OfflinePlayer:" + name)) //nolint:gosec
	h[6] = h[6]&0x0f | 0x30
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

var uuidHexRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ParseUUID accepts a player UUID with or without hyphens and returns its canonical
// lowercase hyphenated form.
func ParseUUID(s string) (string, error) {
	h := strings.ToLower(s)
	if len(h) == 36 && h[8] == '-' && h[13] == '-' && h[18] == '-' && h[23] == '-' {
		h = strings.ReplaceAll(h, "-", "")
	}
	if !uuidHexRE.MatchString(h) {
		return "", &ValidationError{Field: "uuid", Message: "not a valid UUID"}
	}
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// IsOfflineUUID reports whether a canonical UUID is a version 3 (offline-mode) UUID, which
// has no Mojang account behind it.
func IsOfflineUUID(uuid string) bool { return len(uuid) == 36 && uuid[14] == '3' }

// BanTimestamp formats a time the way Minecraft writes "created" in its ban lists.
func BanTimestamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05 -0700") }

// ParsePlayerList decodes the JSON array of a player list file. Empty content is an empty list.
func ParsePlayerList[T any](file, content string) ([]T, error) {
	if strings.TrimSpace(content) == "" {
		return []T{}, nil
	}
	var list []T
	if err := json.Unmarshal([]byte(content), &list); err != nil {
		return nil, &ValidationError{Field: file, Message: "is not a valid player list; fix or delete the file"}
	}
	if list == nil {
		list = []T{}
	}
	return list, nil
}

// EncodePlayerList serializes a list the way Minecraft does (indented JSON array).
func EncodePlayerList[T any](list []T) string {
	if len(list) == 0 {
		return "[]\n"
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	return string(b) + "\n"
}

// PropertyValue returns the value of key in server.properties content.
func PropertyValue(content, key string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

// SetProperty replaces the value of key in server.properties content, or appends it,
// leaving every other line (comments, order, line endings) untouched.
func SetProperty(content, key, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		bare := strings.TrimRight(line, "\r")
		if k, _, ok := strings.Cut(bare, "="); ok && k == key {
			lines[i] = key + "=" + value + line[len(bare):]
			return strings.Join(lines, "\n")
		}
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + key + "=" + value + "\n"
}
