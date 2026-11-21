package filesystem

import (
	"archive/zip"
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// WriteZip streams directory dir as a zip archive to w, entries prefixed with the
// directory's name ("server" for the root). Symlinks and special files are skipped, so the
// archive only ever contains what is inside the server directory. Files changed while the
// archive is written may be captured inconsistently.
func (s *ServerFiles) WriteZip(id, dir string, w io.Writer) error {
	r, err := s.open(id)
	if err != nil {
		return err
	}
	defer r.Close()
	if info, err := r.Stat(name(dir)); err != nil {
		return mapError(err)
	} else if !info.IsDir() {
		return domain.ErrNotDirectory
	}
	prefix := path.Base("/" + dir)
	if dir == "" {
		prefix = "server"
	}
	return writeZip(r.FS(), dir, prefix, w, false)
}

// writeZip streams directory dir of fsys to w as a zip archive, entry names prefixed with
// prefix ("" for none). Only directories and regular files are archived, never the panel's
// own temporary files. fast trades compression ratio for CPU.
func writeZip(fsys fs.FS, dir, prefix string, w io.Writer, fast bool) error {
	zw := zip.NewWriter(w)
	if fast {
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(w, flate.BestSpeed)
		})
	}
	err := fs.WalkDir(fsys, name(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p != name(dir) && errors.Is(err, fs.ErrNotExist) {
				return nil // removed while archiving (e.g. a rotated log)
			}
			return err
		}
		entry := path.Join(prefix, relativeTo(dir, p))
		switch {
		case entry == "":
			return nil // the root itself, without a prefix
		case d.Name() == domain.ModpackSupportedFile:
			return nil // container capability belongs to the runtime, not a portable backup
		case strings.HasPrefix(d.Name(), tempPrefix):
			return nil
		case d.IsDir():
			_, err := zw.Create(entry + "/")
			return err
		case !d.Type().IsRegular():
			return nil // symlinks and special files are never archived
		}
		f, err := fsys.Open(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name, hdr.Method = entry, zip.Deflate
		dst, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		_, err = io.Copy(dst, f)
		return err
	})
	if err != nil {
		return mapError(err)
	}
	return zw.Close()
}

// relativeTo returns walk path p relative to dir ("" for dir itself).
func relativeTo(dir, p string) string {
	switch {
	case p == name(dir):
		return ""
	case dir == "":
		return p
	}
	return strings.TrimPrefix(p, dir+"/")
}

type extractEntry struct {
	file *zip.File
	path string // cleaned, relative to the server root
	dir  bool
}

// Extract unpacks the zip archive at archive into directory dest and returns the number of
// entries written. Entry names are validated like any server path (no "..", no absolute
// paths); names without the UTF-8 flag are decoded as CP437, as the zip format specifies.
// Symlinks are skipped, and both the entry count and the bytes actually decompressed are
// limited, whatever the archive headers claim. Conflicts are checked before anything is
// written; a failure in the middle (e.g. a corrupt entry) leaves the entries already written.
func (s *ServerFiles) Extract(id, archive, dest string, overwrite bool) (int, error) {
	r, err := s.open(id)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	if info, err := r.Stat(name(dest)); err != nil {
		return 0, mapError(err)
	} else if !info.IsDir() {
		return 0, domain.ErrNotDirectory
	}
	f, err := r.Open(archive)
	if err != nil {
		return 0, mapError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, mapError(err)
	}
	if !info.Mode().IsRegular() {
		return 0, domain.ErrInvalidArchive
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return 0, fmt.Errorf("%w: %v", domain.ErrInvalidArchive, err)
	}
	return s.extractZip(context.Background(), r, zr, dest, overwrite,
		extractLimits{entries: domain.ExtractEntriesMax, bytes: domain.ExtractMaxBytes})
}

// extractLimits bound an extraction: entries in the archive and bytes really decompressed.
type extractLimits struct {
	entries int
	bytes   int64
}

// extractZip unpacks zr into directory dest of r (see Extract). ctx is checked between
// entries.
func (s *ServerFiles) extractZip(ctx context.Context, r *os.Root, zr *zip.Reader, dest string, overwrite bool, lim extractLimits) (int, error) {
	if len(zr.File) > lim.entries {
		return 0, fmt.Errorf("%w: %d entries", domain.ErrArchiveTooLarge, len(zr.File))
	}

	// Validate every entry and check conflicts before writing anything.
	var entries []extractEntry
	var declared uint64
	for _, zf := range zr.File {
		entryName := zf.Name
		if zf.NonUTF8 {
			entryName = decodeCP437(entryName) // the zip default when the UTF-8 flag is unset
		}
		if strings.HasPrefix(entryName, "/") {
			return 0, fmt.Errorf("%w: absolute entry %q", domain.ErrInvalidArchive, entryName)
		}
		clean, err := domain.CleanServerPath("entry", entryName)
		if err != nil {
			return 0, fmt.Errorf("%w: entry %q: %v", domain.ErrInvalidArchive, entryName, err)
		}
		mode := zf.Mode()
		if clean == "" || mode&fs.ModeSymlink != 0 || (!mode.IsDir() && !mode.IsRegular()) {
			continue
		}
		target := path.Join(dest, clean)
		e := extractEntry{file: zf, path: target, dir: mode.IsDir()}
		existing, err := r.Lstat(target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return 0, mapError(err)
		case e.dir && !existing.IsDir():
			return 0, fmt.Errorf("%w: %s", domain.ErrNotDirectory, target)
		case !e.dir && existing.IsDir():
			return 0, fmt.Errorf("%w: %s", domain.ErrIsDirectory, target)
		case !e.dir && !overwrite:
			return 0, fmt.Errorf("%w: %s", domain.ErrFileExists, target)
		}
		declared += zf.UncompressedSize64
		if declared > uint64(lim.bytes) {
			return 0, fmt.Errorf("%w: more than %d bytes", domain.ErrArchiveTooLarge, lim.bytes)
		}
		entries = append(entries, e)
	}

	budget := lim.bytes
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if e.dir {
			if err := s.mkdirAll(r, e.path); err != nil {
				return 0, err
			}
			continue
		}
		if err := s.mkdirAll(r, path.Dir(e.path)); err != nil {
			return 0, err
		}
		n, err := s.extractFile(r, e, budget)
		if err != nil {
			return 0, err
		}
		budget -= n
	}
	return len(entries), nil
}

// extractFile writes one entry atomically, decompressing at most budget bytes.
func (s *ServerFiles) extractFile(r *os.Root, e extractEntry, budget int64) (int64, error) {
	rc, err := e.file.Open()
	if err != nil {
		return 0, fmt.Errorf("%w: %s: %v", domain.ErrInvalidArchive, e.file.Name, err)
	}
	defer rc.Close()
	er := &entryReader{r: rc, left: budget}
	if err := s.replace(r, e.path, er, fileMode); err != nil {
		return 0, err
	}
	return er.n, nil
}

// entryReader counts the bytes actually decompressed and fails with ErrArchiveTooLarge past
// the budget (headers can lie). Decompression and checksum errors become ErrInvalidArchive.
type entryReader struct {
	r    io.Reader
	left int64
	n    int64
}

func (e *entryReader) Read(p []byte) (int, error) {
	if int64(len(p)) > e.left+1 {
		p = p[:e.left+1] // one byte past the budget is enough to detect an overflow
	}
	k, err := e.r.Read(p)
	if int64(k) > e.left {
		return 0, domain.ErrArchiveTooLarge
	}
	e.left -= int64(k)
	e.n += int64(k)
	if err != nil && !errors.Is(err, io.EOF) {
		return k, fmt.Errorf("%w: %v", domain.ErrInvalidArchive, err)
	}
	return k, err
}
