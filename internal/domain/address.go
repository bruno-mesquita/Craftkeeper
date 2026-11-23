package domain

import (
	"net/netip"
	"strings"
)

// PublicHostMaxLen is the longest DNS name.
const PublicHostMaxLen = 253

// NormalizePublicHost trims and lowercases the host players use to reach the game servers.
func NormalizePublicHost(host string) string { return strings.ToLower(strings.TrimSpace(host)) }

// ValidatePublicHost accepts "" (none), a DNS name or an IP address, without scheme or port:
// the panel appends each server's port.
func ValidatePublicHost(host string) error {
	if host == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	invalid := &ValidationError{Field: "host", Message: "must be a host name or IP address, without scheme or port"}
	if len(host) > PublicHostMaxLen {
		return invalid
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return invalid
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return invalid
			}
		}
	}
	return nil
}
