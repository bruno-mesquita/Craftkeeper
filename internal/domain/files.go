package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// File manager limits (docs/API.md, "Files").
const (
	ServerPathMaxBytes   = 1024
	ServerPathSegmentMax = 255
	EditableFileMaxBytes = 1 << 20
	ListEntriesMax       = 10_000
	UploadMaxBytes       = 2 << 30
	ExtractEntriesMax    = 100_000
	ExtractMaxBytes      = 8 << 30
)

// FileType is the kind of a directory entry.
type FileType string

const (
	FileTypeFile    FileType = "file"
	FileTypeDir     FileType = "dir"
	FileTypeSymlink FileType = "symlink"
	FileTypeOther   FileType = "other" // devices, sockets, pipes: listed, never opened
)

// FileEntry is one entry of a server directory listing.
type FileEntry struct {
	Name       string
	Type       FileType
	Size       int64
	ModifiedAt time.Time
}

// FileListing is a directory listing, cut at ListEntriesMax entries.
type FileListing struct {
	Path      string
	Entries   []FileEntry
	Truncated bool
}

// FileText is an editable text file. Version changes whenever the file does, so a save can
// detect that someone else (often the game) wrote the file in the meantime.
type FileText struct {
	Path    string
	Content string
	Version string
	Size    int64
}

// CleanServerPath validates a path relative to a server's directory and returns it in a
// canonical form: segments separated by "/", no leading or trailing slash, "" for the root.
// It is for clear errors only; confinement is enforced by the filesystem layer (os.Root).
func CleanServerPath(field, p string) (string, error) {
	invalid := func(msg string) (string, error) {
		return "", &ValidationError{Field: field, Message: msg}
	}
	switch {
	case len(p) > ServerPathMaxBytes:
		return invalid("path is too long")
	case !utf8.ValidString(p):
		return invalid("path must be valid UTF-8")
	case strings.ContainsAny(p, "\x00\\"):
		return invalid("path must not contain NUL or backslashes")
	}
	var parts []string
	for _, seg := range strings.Split(p, "/") {
		switch {
		case seg == "" || seg == ".":
			continue
		case seg == "..":
			return invalid("path must not contain '..'")
		case len(seg) > ServerPathSegmentMax:
			return invalid("a path segment is too long")
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return invalid("path must not contain control characters")
			}
		}
		parts = append(parts, seg)
	}
	return strings.Join(parts, "/"), nil
}

// ValidateFileName checks a single path segment, such as an uploaded file's name.
func ValidateFileName(field, name string) error {
	clean, err := CleanServerPath(field, name)
	if err != nil {
		return err
	}
	if clean == "" || clean != name || strings.Contains(name, "/") {
		return &ValidationError{Field: field, Message: "invalid file name"}
	}
	return nil
}
