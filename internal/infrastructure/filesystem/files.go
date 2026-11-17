package filesystem

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const (
	fileMode fs.FileMode = 0o640
	dirMode  fs.FileMode = 0o750
)

const (
	tempPrefix   = ".mesquita-tmp-" // in-progress uploads and edits
	readDirBatch = 256
)

// ServerFiles gives the file manager access to the contents of server directories. Every
// operation goes through an os.Root opened at <root>/<server id>, so no path, symlink or
// rename can reach outside the server's directory. Paths are relative, "/"-separated and
// already cleaned by domain.CleanServerPath ("" is the server's root).
type ServerFiles struct {
	root     string
	uid, gid int // owner of every file the panel creates: the user the game container runs as
}

func NewServerFiles(root string, uid, gid int) *ServerFiles {
	return &ServerFiles{root: root, uid: uid, gid: gid}
}

func (s *ServerFiles) open(id string) (*os.Root, error) {
	if !domain.ValidServerID(id) {
		return nil, fmt.Errorf("filesystem: invalid server id %q", id)
	}
	r, err := os.OpenRoot(filepath.Join(s.root, id))
	if err != nil {
		return nil, mapError(err)
	}
	return r, nil
}

// name turns a cleaned server path into an os.Root name ("" is the root itself).
func name(p string) string {
	if p == "" {
		return "."
	}
	return p
}

// List lists a directory, directories first, then by name. Symlinks are listed as such and
// never followed. At most domain.ListEntriesMax entries are returned.
func (s *ServerFiles) List(id, dir string) (domain.FileListing, error) {
	r, err := s.open(id)
	if err != nil {
		return domain.FileListing{}, err
	}
	defer r.Close()
	f, err := r.Open(name(dir))
	if err != nil {
		return domain.FileListing{}, mapError(err)
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil {
		return domain.FileListing{}, mapError(err)
	} else if !info.IsDir() {
		return domain.FileListing{}, domain.ErrNotDirectory
	}

	out := domain.FileListing{Path: dir, Entries: []domain.FileEntry{}}
	for {
		batch, err := f.ReadDir(readDirBatch)
		for _, e := range batch {
			if len(out.Entries) == domain.ListEntriesMax {
				out.Truncated = true
				break
			}
			info, err := e.Info()
			if err != nil {
				continue // removed while listing
			}
			out.Entries = append(out.Entries, entryOf(e.Name(), info))
		}
		if out.Truncated || errors.Is(err, io.EOF) || len(batch) == 0 {
			break
		}
		if err != nil {
			return domain.FileListing{}, mapError(err)
		}
	}
	slices.SortFunc(out.Entries, func(a, b domain.FileEntry) int {
		if (a.Type == domain.FileTypeDir) != (b.Type == domain.FileTypeDir) {
			if a.Type == domain.FileTypeDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out, nil
}

func entryOf(n string, info fs.FileInfo) domain.FileEntry {
	e := domain.FileEntry{Name: n, Size: info.Size(), ModifiedAt: info.ModTime()}
	switch m := info.Mode(); {
	case m.IsDir():
		e.Type, e.Size = domain.FileTypeDir, 0
	case m&fs.ModeSymlink != 0:
		e.Type = domain.FileTypeSymlink
	case m.IsRegular():
		e.Type = domain.FileTypeFile
	default:
		e.Type, e.Size = domain.FileTypeOther, 0
	}
	return e
}

// Stat describes a path, following symlinks that stay inside the server's directory.
func (s *ServerFiles) Stat(id, p string) (domain.FileEntry, error) {
	r, err := s.open(id)
	if err != nil {
		return domain.FileEntry{}, err
	}
	defer r.Close()
	info, err := r.Stat(name(p))
	if err != nil {
		return domain.FileEntry{}, mapError(err)
	}
	return entryOf(path.Base("/"+p), info), nil
}

// OpenFile opens a regular file for reading (download). The caller closes it.
func (s *ServerFiles) OpenFile(id, p string) (*os.File, int64, error) {
	r, err := s.open(id)
	if err != nil {
		return nil, 0, err
	}
	defer r.Close() // files opened through a Root stay valid after it is closed
	f, err := r.Open(name(p))
	if err != nil {
		return nil, 0, mapError(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, mapError(err)
	}
	if info.IsDir() {
		f.Close()
		return nil, 0, domain.ErrIsDirectory
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("%w: not a regular file", domain.ErrNotFound)
	}
	return f, info.Size(), nil
}

// version identifies a file's content cheaply. The inode changes on every atomic replace (our
// own writes); mtime and size catch in-place writes such as the game saving its config.
// mtime alone is not enough: the kernel updates it with a coarse clock.
func version(info fs.FileInfo) string {
	var ino uint64
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	return strconv.FormatUint(ino, 36) + "-" + strconv.FormatInt(info.ModTime().UnixNano(), 36) + "-" + strconv.FormatInt(info.Size(), 36)
}

// ReadText reads a UTF-8 text file of at most domain.EditableFileMaxBytes for the editor.
func (s *ServerFiles) ReadText(id, p string) (domain.FileText, error) {
	r, err := s.open(id)
	if err != nil {
		return domain.FileText{}, err
	}
	defer r.Close()
	info, err := editable(r, p)
	if err != nil {
		return domain.FileText{}, err
	}
	if info == nil {
		return domain.FileText{}, fmt.Errorf("%w: %s", domain.ErrNotFound, p)
	}
	if info.Size() > domain.EditableFileMaxBytes {
		return domain.FileText{}, domain.ErrFileTooLarge
	}
	f, err := r.Open(p)
	if err != nil {
		return domain.FileText{}, mapError(err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, domain.EditableFileMaxBytes+1))
	if err != nil {
		return domain.FileText{}, mapError(err)
	}
	if len(b) > domain.EditableFileMaxBytes {
		return domain.FileText{}, domain.ErrFileTooLarge
	}
	if !utf8.Valid(b) || slices.Contains(b, 0) {
		return domain.FileText{}, domain.ErrNotText
	}
	return domain.FileText{Path: p, Content: string(b), Version: version(info), Size: int64(len(b))}, nil
}

// editable returns the file's info (nil when it does not exist), refusing what the editor
// must not touch: directories, symlinks and special files.
func editable(r *os.Root, p string) (fs.FileInfo, error) {
	if p == "" {
		return nil, domain.ErrIsDirectory
	}
	info, err := r.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, mapError(err)
	case info.IsDir():
		return nil, domain.ErrIsDirectory
	case info.Mode()&fs.ModeSymlink != 0:
		return nil, &domain.ValidationError{Field: "path", Message: "symlinks cannot be edited; edit their target"}
	case !info.Mode().IsRegular():
		return nil, &domain.ValidationError{Field: "path", Message: "not a regular file"}
	}
	return info, nil
}

// WriteText replaces (or creates) a text file atomically and returns its new version. A
// non-empty expected version must match the file's current one (domain.ErrFileChanged).
// An existing file keeps its permissions.
func (s *ServerFiles) WriteText(id, p, content, expected string) (string, error) {
	if len(content) > domain.EditableFileMaxBytes {
		return "", domain.ErrFileTooLarge
	}
	r, err := s.open(id)
	if err != nil {
		return "", err
	}
	defer r.Close()
	info, err := editable(r, p)
	if err != nil {
		return "", err
	}
	mode := fileMode
	switch {
	case info != nil && expected != "" && version(info) != expected:
		return "", domain.ErrFileChanged
	case info == nil && expected != "":
		return "", domain.ErrFileChanged // deleted since it was read
	case info != nil:
		mode = info.Mode().Perm()
	}
	if err := s.replace(r, p, strings.NewReader(content), mode); err != nil {
		return "", err
	}
	info, err = r.Lstat(p)
	if err != nil {
		return "", mapError(err)
	}
	return version(info), nil
}

// Save stores an uploaded file named n in directory dir, streaming src to disk.
func (s *ServerFiles) Save(id, dir, n string, src io.Reader, overwrite bool) (domain.FileEntry, error) {
	r, err := s.open(id)
	if err != nil {
		return domain.FileEntry{}, err
	}
	defer r.Close()
	if info, err := r.Stat(name(dir)); err != nil {
		return domain.FileEntry{}, mapError(err)
	} else if !info.IsDir() {
		return domain.FileEntry{}, domain.ErrNotDirectory
	}
	target := path.Join(dir, n)
	if err := checkTarget(r, target, overwrite); err != nil {
		return domain.FileEntry{}, err
	}
	if err := s.replace(r, target, src, fileMode); err != nil {
		return domain.FileEntry{}, err
	}
	info, err := r.Lstat(target)
	if err != nil {
		return domain.FileEntry{}, mapError(err)
	}
	return entryOf(n, info), nil
}

// checkTarget allows writing a file at p: it must not be a directory, and must not exist
// unless overwrite is set. (A file created between this check and the rename is replaced.)
func checkTarget(r *os.Root, p string, overwrite bool) error {
	info, err := r.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return mapError(err)
	case info.IsDir():
		return domain.ErrIsDirectory
	case !overwrite:
		return fmt.Errorf("%w: %s", domain.ErrFileExists, p)
	}
	return nil
}

// replace writes src to a temporary file next to p, then renames it over p, so readers
// (including the game) never see a partial file. The temporary file is removed on failure.
func (s *ServerFiles) replace(r *os.Root, p string, src io.Reader, mode fs.FileMode) (err error) {
	tmp := path.Join(path.Dir(p), tempPrefix+rand.Text())
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return mapError(err)
	}
	defer func() {
		if err != nil {
			f.Close()
			r.Remove(tmp)
		}
	}()
	if _, err = io.Copy(f, src); err != nil {
		return mapError(err)
	}
	if err = f.Sync(); err != nil {
		return mapError(err)
	}
	if err = f.Close(); err != nil {
		return mapError(err)
	}
	if err = r.Chmod(tmp, mode); err != nil { // OpenFile's mode is reduced by the umask
		return mapError(err)
	}
	if err = r.Lchown(tmp, s.uid, s.gid); err != nil {
		return mapError(err)
	}
	if err = r.Rename(tmp, p); err != nil {
		return mapError(err)
	}
	return nil
}

// Mkdir creates one directory; its parent must exist.
func (s *ServerFiles) Mkdir(id, p string) error {
	r, err := s.open(id)
	if err != nil {
		return err
	}
	defer r.Close()
	return s.mkdir(r, p)
}

func (s *ServerFiles) mkdir(r *os.Root, p string) error {
	if err := r.Mkdir(p, dirMode); err != nil {
		return mapError(err)
	}
	if err := r.Lchown(p, s.uid, s.gid); err != nil {
		return mapError(err)
	}
	return nil
}

// mkdirAll creates p and its missing parents, each owned by the container user.
func (s *ServerFiles) mkdirAll(r *os.Root, p string) error {
	if p == "" {
		return nil
	}
	cur := ""
	for _, seg := range strings.Split(p, "/") {
		cur = path.Join(cur, seg)
		info, err := r.Lstat(cur)
		switch {
		case err == nil && info.IsDir():
			continue
		case err == nil:
			return fmt.Errorf("%w: %s", domain.ErrNotDirectory, cur)
		case !errors.Is(err, fs.ErrNotExist):
			return mapError(err)
		}
		if err := s.mkdir(r, cur); err != nil && !errors.Is(err, domain.ErrFileExists) {
			return err
		}
	}
	return nil
}

// Rename moves from to to (both non-root). An existing target is never replaced.
func (s *ServerFiles) Rename(id, from, to string) error {
	r, err := s.open(id)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(from); err != nil {
		return mapError(err)
	}
	if _, err := r.Lstat(to); err == nil {
		return fmt.Errorf("%w: %s", domain.ErrFileExists, to)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return mapError(err)
	}
	return mapError(r.Rename(from, to))
}

// Remove deletes a file, symlink (not its target) or directory tree (non-root).
func (s *ServerFiles) Remove(id, p string) error {
	r, err := s.open(id)
	if err != nil {
		return err
	}
	defer r.Close()
	if _, err := r.Lstat(p); err != nil {
		return mapError(err)
	}
	return mapError(r.RemoveAll(p))
}

// mapError translates filesystem errors to domain errors.
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ELOOP):
		return fmt.Errorf("%w: %v", domain.ErrNotFound, err)
	case errors.Is(err, fs.ErrExist):
		return fmt.Errorf("%w: %v", domain.ErrFileExists, err)
	case errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%w: %v", domain.ErrNotDirectory, err)
	case errors.Is(err, syscall.EISDIR):
		return fmt.Errorf("%w: %v", domain.ErrIsDirectory, err)
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return fmt.Errorf("%w: %v", domain.ErrInsufficientStorage, err)
	case strings.Contains(err.Error(), "path escapes from parent"):
		// os.Root refused a path or symlink leading outside the server directory.
		return &domain.ValidationError{Field: "path", Message: "path leads outside the server directory"}
	}
	var ve *domain.ValidationError
	if errors.As(err, &ve) {
		return err
	}
	return fmt.Errorf("filesystem: %w", err)
}
