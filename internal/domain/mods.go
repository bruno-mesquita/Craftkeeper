package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Panel-owned marker files at the root of a server directory. ModpackConfigFile holds the
// selected installer and startup scripts; its presence means the pack, not the panel, owns the
// version and the mods. ModpackInstalledFile records that the installer completed.
// ModpackSupportedFile marks a server whose container has the modpack wrapper.
const (
	ModpackConfigFile    = ".craftkeeper-modpack"
	ModpackInstalledFile = ".craftkeeper-modpack-installed"
	ModpackSupportedFile = ".craftkeeper-modpack-supported"
)

// ModsDir is the directory, relative to the server root, the mod loaders read mods from.
const ModsDir = "mods"

// DisabledModSuffix is appended to a mod's file name to keep the loader from loading it.
const DisabledModSuffix = ".disabled"

// ModsPerOperation bounds how many mods one install or update downloads, dependencies included.
const ModsPerOperation = 32

// ModFileMaxBytes bounds one downloaded mod file.
const ModFileMaxBytes = 256 << 20

// Dependency kinds, as Modrinth names them.
const (
	ModDependencyRequired     = "required"
	ModDependencyOptional     = "optional"
	ModDependencyIncompatible = "incompatible"
	ModDependencyEmbedded     = "embedded"
)

// ModVersionRelease is the Modrinth version type preferred when picking a version.
const ModVersionRelease = "release"

// ModProject is a mod published on Modrinth.
type ModProject struct {
	ID          string
	Slug        string
	Title       string
	Description string
	IconURL     string // "" when the project has no icon
	ServerSide  string // "required", "optional", "unsupported" or "unknown"
	Downloads   int64
}

// ModFile is the jar a mod version publishes.
type ModFile struct {
	URL    string
	Name   string
	SHA512 string // lowercase hex
	Size   int64
}

type ModDependency struct {
	ProjectID string // "" when Modrinth only names the version
	VersionID string // "" when any version of the project will do
	Kind      string
}

// ModVersion is one release of a mod, with the file a server installs.
type ModVersion struct {
	ID           string
	ProjectID    string
	Name         string
	Number       string
	Type         string // "release", "beta" or "alpha"
	Loaders      []string
	GameVersions []string
	Published    time.Time
	File         ModFile
	Dependencies []ModDependency
}

// Supports reports whether the version runs on loader with Minecraft mc.
func (v ModVersion) Supports(loader, mc string) bool {
	return slices.Contains(v.Loaders, loader) && slices.Contains(v.GameVersions, mc)
}

// ModSearch is one page of Modrinth search results.
type ModSearch struct {
	Hits   []ModProject
	Offset int
	Total  int
}

// InstalledMod is a jar in a server's mods directory. Project and Version are known when
// Modrinth recognizes the file's hash; Update is set when a newer compatible version exists.
type InstalledMod struct {
	File       string // file name in ModsDir, with DisabledModSuffix when disabled
	Enabled    bool
	Size       int64
	ModifiedAt time.Time
	Project    *ModProject
	Version    *ModVersion
	Update     *ModVersion
}

var (
	modrinthID = regexp.MustCompile(`^[A-Za-z0-9]{8}$`)
	sha512Hex  = regexp.MustCompile(`^[0-9a-f]{128}$`)
	unsafeMod  = regexp.MustCompile(`[^A-Za-z0-9._+\-]`)
)

// ValidateModrinthID accepts a Modrinth project or version ID (8 base62 characters).
func ValidateModrinthID(field, id string) error {
	if !modrinthID.MatchString(id) {
		return &ValidationError{Field: field, Message: "is not a Modrinth ID"}
	}
	return nil
}

// ValidSHA512 reports whether h is a lowercase hex SHA-512 digest.
func ValidSHA512(h string) bool { return sha512Hex.MatchString(h) }

// ValidateModFile accepts the name of a file in ModsDir that the panel manages: a single
// path element ending in ".jar" or ".jar.disabled". It returns whether the mod is enabled.
func ValidateModFile(name string) (enabled bool, err error) {
	bad := &ValidationError{Field: "file", Message: `must be a ".jar" or ".jar.disabled" file name`}
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || strings.HasPrefix(name, ".") ||
		strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl) {
		return false, bad
	}
	switch {
	case strings.HasSuffix(name, ".jar") && len(name) > len(".jar"):
		return true, nil
	case strings.HasSuffix(name, ".jar"+DisabledModSuffix) && len(name) > len(".jar"+DisabledModSuffix):
		return false, nil
	}
	return false, bad
}

// SafeModFileName turns the file name a mod publishes into one the panel writes: characters
// outside [A-Za-z0-9._+-] become "_" and leading dots are dropped. Names that do not end in
// ".jar" are refused, since the loader would not load them.
func SafeModFileName(name string) (string, error) {
	name = unsafeMod.ReplaceAllString(name, "_")
	if len(name) > 200 {
		name = name[len(name)-200:]
	}
	name = strings.TrimLeft(name, ".")
	if _, err := ValidateModFile(name); err != nil || !strings.HasSuffix(name, ".jar") {
		return "", &ValidationError{Field: "file", Message: "the mod's file is not a jar"}
	}
	return name, nil
}
