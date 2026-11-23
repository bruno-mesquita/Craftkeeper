package domain

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// Modpack sources: the catalogs a server's modpack can come from. Persisted and returned by
// the API.
const (
	ModpackSourceModrinth   = "modrinth"
	ModpackSourceCurseForge = "curseforge"
)

// CurseForgeKeyMaxLen bounds the CurseForge API key the panel stores.
const CurseForgeKeyMaxLen = 128

// ModpackRef is the catalog modpack a server runs. The game image installs it on start and,
// when VersionID changes, replaces the previous version's files (never the world). Zero
// value: the server runs no catalog modpack.
type ModpackRef struct {
	Source      string // ModpackSource*; "" for none
	ProjectID   string // Modrinth project ID or CurseForge mod ID
	Slug        string // project slug (CurseForge selects packs by slug)
	VersionID   string // Modrinth version ID or CurseForge file ID
	Name        string // project title, for display
	VersionName string // version name, for display
}

// Set reports whether the server runs a catalog modpack.
func (m ModpackRef) Set() bool { return m.Source != "" }

// ModpackVersion is one version of a modpack that the panel can run: its loader is one the
// panel supports and its Minecraft version is a release.
type ModpackVersion struct {
	ID          string
	Name        string
	Type        string // "release", "beta" or "alpha"
	Published   time.Time
	Loader      string // Loader*
	GameVersion string // Minecraft release
}

var (
	curseForgeID = regexp.MustCompile(`^[1-9][0-9]{0,11}$`)
	projectSlug  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)
)

// ValidateModpackSource accepts the catalogs the panel reads.
func ValidateModpackSource(source string) error {
	switch source {
	case ModpackSourceModrinth, ModpackSourceCurseForge:
		return nil
	}
	return &ValidationError{Field: "source", Message: `must be "modrinth" or "curseforge"`}
}

// ValidateModpackID accepts a project or version ID of source. These IDs end up in the game
// image's environment, so only the catalogs' exact formats are accepted.
func ValidateModpackID(source, field, id string) error {
	if err := ValidateModpackSource(source); err != nil {
		return err
	}
	if source == ModpackSourceModrinth {
		return ValidateModrinthID(field, id)
	}
	if !curseForgeID.MatchString(id) {
		return &ValidationError{Field: field, Message: "is not a CurseForge ID"}
	}
	return nil
}

// ValidProjectSlug reports whether s has the shape of a catalog project slug.
func ValidProjectSlug(s string) bool { return projectSlug.MatchString(s) }

// ValidateCurseForgeKey accepts printable ASCII without spaces, as CurseForge issues keys.
func ValidateCurseForgeKey(key string) error {
	if key == "" || len(key) > CurseForgeKeyMaxLen {
		return &ValidationError{Field: "api_key", Message: "must be 1 to 128 characters"}
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return &ValidationError{Field: "api_key", Message: "must be printable ASCII without spaces"}
		}
	}
	return nil
}

// ModpackGame picks the loader and Minecraft release a modpack version runs, from the tags a
// catalog publishes (case-insensitive loader names; CurseForge mixes them with game versions).
// ok is false when no supported loader or no release is listed. With several releases, the
// newest wins.
func ModpackGame(tags []string) (loader, mc string, ok bool) {
	for _, t := range tags {
		switch l := strings.ToLower(t); l {
		case LoaderFabric, LoaderForge, LoaderNeoForge:
			if loader == "" || l == LoaderNeoForge { // a NeoForge pack may also list Forge
				loader = l
			}
		default:
			if IsMinecraftRelease(t) && (mc == "" || CompareVersions(t, mc) > 0) {
				mc = t
			}
		}
	}
	return loader, mc, loader != "" && mc != ""
}

// SortModpackVersions orders versions newest first.
func SortModpackVersions(v []ModpackVersion) {
	slices.SortStableFunc(v, func(a, b ModpackVersion) int { return b.Published.Compare(a.Published) })
}
