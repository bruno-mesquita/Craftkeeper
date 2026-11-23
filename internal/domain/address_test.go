package domain

import (
	"strings"
	"testing"
)

func TestValidatePublicHost(t *testing.T) {
	valid := []string{"", "minecraft.brunomesquita.dev", "localhost", "play-1.example.com", "192.168.15.11", "100.126.54.17", "::1", "fd7a:115c::1"}
	for _, h := range valid {
		if err := ValidatePublicHost(h); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
	invalid := []string{"http://x.com", "x.com:25565", "a b", "-x.com", "x-.com", "x..com", ".", "x.com/path",
		"[::1]", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "aa", "é.com"}
	for _, h := range invalid {
		if err := ValidatePublicHost(h); err == nil {
			t.Errorf("%q: accepted", h)
		}
	}
}
