package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanServerPath(t *testing.T) {
	ok := map[string]string{
		"":                        "",
		"/":                       "",
		".":                       "",
		"world":                   "world",
		"/world/region/":          "world/region",
		"./a//b/./c":              "a/b/c",
		"plugins/Essentials.jar":  "plugins/Essentials.jar",
		"names with spaces/c.txt": "names with spaces/c.txt",
		"..hidden/file..txt":      "..hidden/file..txt",
	}
	for in, want := range ok {
		got, err := CleanServerPath("path", in)
		if err != nil || got != want {
			t.Errorf("CleanServerPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"..", "../etc", "world/../../etc", "a/..", "a\\..\\b", "a\x00b", "a\nb", "a\x7fb",
		"\xff", strings.Repeat("a", ServerPathMaxBytes+1), strings.Repeat("b", ServerPathSegmentMax+1),
	}
	for _, in := range bad {
		_, err := CleanServerPath("path", in)
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != "path" {
			t.Errorf("CleanServerPath(%q) = %v, want a validation error", in, err)
		}
	}
}

func TestValidateFileName(t *testing.T) {
	for _, n := range []string{"a.jar", ".hidden", "with space.txt"} {
		if err := ValidateFileName("name", n); err != nil {
			t.Errorf("ValidateFileName(%q) = %v", n, err)
		}
	}
	for _, n := range []string{"", ".", "..", "a/b", "/a", "a/", "a\\b"} {
		if err := ValidateFileName("name", n); err == nil {
			t.Errorf("ValidateFileName(%q) accepted", n)
		}
	}
}
