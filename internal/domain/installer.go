package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// Loader identifiers: the server types the panel can install. They are persisted and
// returned by the API.
const (
	LoaderVanilla  = "vanilla"
	LoaderFabric   = "fabric"
	LoaderForge    = "forge"
	LoaderNeoForge = "neoforge"
)

// MinecraftLatest is the vanilla-only version that follows the newest release on every start.
const MinecraftLatest = "LATEST"

// Java runtimes. Each one maps to a game image pinned by the container runtime.
const (
	Java8  = "java8"
	Java17 = "java17"
	Java21 = "java21"
	Java25 = "java25"
)

// LoaderVersions lists the builds of a loader for one Minecraft release, newest first.
type LoaderVersions struct {
	Versions    []string
	Recommended string
}

// Loader versions end up in the game image's environment, which its scripts use in shell
// commands and URLs, so only plain dotted numbers with an optional "-beta" are accepted.
var loaderVersion = regexp.MustCompile(`^[0-9]{1,4}(\.[0-9]{1,6}){1,3}(-beta)?$`)

// ValidateLoader accepts the server types the panel can install.
func ValidateLoader(loader string) error {
	switch loader {
	case LoaderVanilla, LoaderFabric, LoaderForge, LoaderNeoForge:
		return nil
	}
	return &ValidationError{Field: "loader", Message: `must be "vanilla", "fabric", "forge" or "neoforge"`}
}

// Modded reports whether loader installs a mod loader on top of the game.
func Modded(loader string) bool { return loader != LoaderVanilla }

// IsMinecraftRelease reports whether v names one release (not "LATEST").
func IsMinecraftRelease(v string) bool {
	return v != MinecraftLatest && minecraftVersion.MatchString(v)
}

// ValidLoaderVersion reports whether v has the shape of a loader build number.
func ValidLoaderVersion(v string) bool { return loaderVersion.MatchString(v) }

// CompareVersions orders dotted version numbers component by component (missing components
// count as 0). A "-beta" build sorts before the release with the same numbers.
// Inputs are expected to be valid Minecraft or loader versions.
func CompareVersions(a, b string) int {
	a, aBeta := strings.CutSuffix(a, "-beta")
	b, bBeta := strings.CutSuffix(b, "-beta")
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(as), len(bs)) {
		x, y := component(as, i), component(bs, i)
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case aBeta == bBeta:
		return 0
	case aBeta:
		return -1
	}
	return 1
}

func component(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n, _ := strconv.Atoi(parts[i])
	return n
}

// JavaFor picks the Java runtime a Minecraft version needs: Java 8 up to 1.16.5 (old Forge
// does not run on newer Java), 17 up to 1.20.4, 21 for 1.20.5–1.21.x, and 25 after that and
// for "LATEST".
func JavaFor(mcVersion string) string {
	if mcVersion == MinecraftLatest {
		return Java25
	}
	switch {
	case CompareVersions(mcVersion, "1.16.5") <= 0:
		return Java8
	case CompareVersions(mcVersion, "1.20.4") <= 0:
		return Java17
	case CompareVersions(mcVersion, "1.22") < 0:
		return Java21
	}
	return Java25
}
