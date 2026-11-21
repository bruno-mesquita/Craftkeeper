package filesystem

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

const (
	backupExt     = ".zip"
	restorePrefix = ".restore-" // staging directories of restores, in the servers root
	// restoreEntriesMax bounds the entries of a backup being restored. Backups are made by the
	// panel, so this is far above the file manager's limit, but never unbounded.
	restoreEntriesMax = 2_000_000
)

// Backups stores backup archives in <root>/<server id>/<backup id>.zip, outside every
// server directory (the file manager cannot reach them), owned by the panel's user.
// Archives are made from, and restored into, server directories through ServerFiles.
type Backups struct {
	root  string
	files *ServerFiles
	now   func() time.Time
}

func NewBackups(root string, files *ServerFiles) *Backups {
	return &Backups{root: root, files: files, now: time.Now}
}

func validIDs(serverID, backupID string) error {
	if !domain.ValidServerID(serverID) || !domain.ValidBackupID(backupID) {
		return fmt.Errorf("filesystem: invalid server or backup id %q/%q", serverID, backupID)
	}
	return nil
}

func archiveName(serverID, backupID string) string {
	return path.Join(serverID, backupID+backupExt)
}

func (b *Backups) openRoot() (*os.Root, error) {
	if err := os.MkdirAll(b.root, 0o750); err != nil {
		return nil, fmt.Errorf("filesystem: %w", err)
	}
	r, err := os.OpenRoot(b.root)
	if err != nil {
		return nil, fmt.Errorf("filesystem: %w", err)
	}
	return r, nil
}

// Write archives the whole data directory of a server as backup backupID and returns the
// archive's size. The archive is written to a temporary file, synced and renamed into place,
// so a failure (or ctx being cancelled) never leaves a partial backup behind.
func (b *Backups) Write(ctx context.Context, serverID, backupID string) (size int64, err error) {
	if err := validIDs(serverID, backupID); err != nil {
		return 0, err
	}
	src, err := b.files.open(serverID)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	r, err := b.openRoot()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	if err := r.Mkdir(serverID, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return 0, mapError(err)
	}

	tmp := path.Join(serverID, tempPrefix+rand.Text())
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, mapError(err)
	}
	defer func() {
		if err != nil {
			f.Close()
			r.Remove(tmp)
		}
	}()
	if err = writeZip(src.FS(), "", "", ctxWriter{ctx: ctx, w: f}, true); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
	if err = f.Sync(); err != nil {
		return 0, mapError(err)
	}
	info, err := f.Stat()
	if err != nil {
		return 0, mapError(err)
	}
	if err = f.Close(); err != nil {
		return 0, mapError(err)
	}
	if err = r.Rename(tmp, archiveName(serverID, backupID)); err != nil {
		return 0, mapError(err)
	}
	return info.Size(), nil
}

// ctxWriter stops a long write as soon as ctx is cancelled (panel shutdown).
type ctxWriter struct {
	ctx context.Context
	w   io.Writer
}

func (c ctxWriter) Write(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.w.Write(p)
}

// Open opens a backup archive for reading (download). The caller closes it.
func (b *Backups) Open(serverID, backupID string) (*os.File, int64, error) {
	if err := validIDs(serverID, backupID); err != nil {
		return nil, 0, err
	}
	r, err := b.openRoot()
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	f, err := r.Open(archiveName(serverID, backupID))
	if err != nil {
		return nil, 0, mapError(err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, mapError(err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("%w: not a regular file", domain.ErrNotFound)
	}
	return f, info.Size(), nil
}

// Remove deletes a backup archive. A missing archive is not an error.
func (b *Backups) Remove(serverID, backupID string) error {
	if err := validIDs(serverID, backupID); err != nil {
		return err
	}
	r, err := b.openRoot()
	if err != nil {
		return err
	}
	defer r.Close()
	if err := r.Remove(archiveName(serverID, backupID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return mapError(err)
	}
	return nil
}

// Usage returns the bytes in a server's data directory (regular files, the size a backup
// can reach at most) and the bytes available to the panel on the backups filesystem.
func (b *Backups) Usage(serverID string) (dirBytes, freeBytes int64, err error) {
	src, err := b.files.open(serverID)
	if err != nil {
		return 0, 0, err
	}
	defer src.Close()
	err = fs.WalkDir(src.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p != "." && errors.Is(err, fs.ErrNotExist) {
				return nil // removed while walking
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				dirBytes += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, mapError(err)
	}
	if err := os.MkdirAll(b.root, 0o750); err != nil {
		return 0, 0, fmt.Errorf("filesystem: %w", err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(b.root, &st); err != nil {
		return 0, 0, fmt.Errorf("filesystem: statfs %s: %w", b.root, err)
	}
	return dirBytes, int64(st.Bavail) * st.Bsize, nil
}

// StageRestore extracts a backup into a new staging directory next to the server
// directories and returns its name, for CommitRestore or DiscardRestore. At most maxBytes are
// decompressed (domain.ErrInsufficientStorage past that). The live server directory is not
// touched; on failure the staging directory is removed.
func (b *Backups) StageRestore(ctx context.Context, serverID, backupID string, maxBytes int64) (_ string, err error) {
	if err := validIDs(serverID, backupID); err != nil {
		return "", err
	}
	f, size, err := b.Open(serverID, backupID)
	if err != nil {
		return "", err
	}
	defer f.Close()
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrInvalidArchive, err)
	}

	servers, err := os.OpenRoot(b.files.root)
	if err != nil {
		return "", mapError(err)
	}
	defer servers.Close()
	dir := restorePrefix + serverID + "-" + rand.Text()
	if err := b.files.mkdir(servers, dir); err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			servers.RemoveAll(dir)
		}
	}()
	dst, err := servers.OpenRoot(dir)
	if err != nil {
		return "", mapError(err)
	}
	defer dst.Close()
	_, err = b.files.extractZip(ctx, dst, zr, "", false, extractLimits{entries: restoreEntriesMax, bytes: maxBytes})
	if errors.Is(err, domain.ErrArchiveTooLarge) {
		return "", fmt.Errorf("%w: %v", domain.ErrInsufficientStorage, err)
	}
	if err != nil {
		return "", err
	}
	return dir, nil
}

// CommitRestore swaps a staged restore in: the server's current directory moves to
// .deleted/<id>-<unix>-pre-restore (recoverable by hand), the staging directory takes its
// place. If the second rename fails the current directory is put back.
func (b *Backups) CommitRestore(serverID, staging string) error {
	if !domain.ValidServerID(serverID) || !validStaging(serverID, staging) {
		return fmt.Errorf("filesystem: invalid restore %q/%q", serverID, staging)
	}
	r, err := os.OpenRoot(b.files.root)
	if err != nil {
		return mapError(err)
	}
	defer r.Close()
	if err := r.Mkdir(deletedDir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
		return mapError(err)
	}
	old := path.Join(deletedDir, serverID+"-"+strconv.FormatInt(b.now().Unix(), 10)+"-pre-restore")
	moved := true
	if err := r.Rename(serverID, old); errors.Is(err, fs.ErrNotExist) {
		moved = false
	} else if err != nil {
		return mapError(err)
	}
	if err := r.Rename(staging, serverID); err != nil {
		if moved {
			_ = r.Rename(old, serverID)
		}
		return mapError(err)
	}
	return nil
}

// DiscardRestore removes a staging directory.
func (b *Backups) DiscardRestore(serverID, staging string) error {
	if !validStaging(serverID, staging) {
		return fmt.Errorf("filesystem: invalid restore %q/%q", serverID, staging)
	}
	r, err := os.OpenRoot(b.files.root)
	if err != nil {
		return mapError(err)
	}
	defer r.Close()
	return mapError(r.RemoveAll(staging))
}

func validStaging(serverID, staging string) bool {
	rest, ok := strings.CutPrefix(staging, restorePrefix+serverID+"-")
	return ok && rest != "" && !strings.ContainsAny(rest, "/.")
}

// CleanupStale removes what an interrupted panel left behind: restore staging directories,
// temporary files at the top of server directories (world imports) and temporary backup
// archives. Run at startup, when nothing is in progress.
func (b *Backups) CleanupStale() error {
	var errs []error
	if r, err := os.OpenRoot(b.files.root); err == nil {
		errs = append(errs, removeMatching(r, ".", restorePrefix))
		// Unfinished uploads, edits and world imports at the top of each server directory.
		entries, err := fs.ReadDir(r.FS(), ".")
		errs = append(errs, err)
		for _, e := range entries {
			if e.IsDir() && domain.ValidServerID(e.Name()) {
				errs = append(errs, removeMatching(r, e.Name(), tempPrefix))
			}
		}
		r.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if r, err := os.OpenRoot(b.root); err == nil {
		entries, err := fs.ReadDir(r.FS(), ".")
		errs = append(errs, err)
		for _, e := range entries {
			if e.IsDir() && domain.ValidServerID(e.Name()) {
				errs = append(errs, removeMatching(r, e.Name(), tempPrefix))
			}
		}
		r.Close()
	} else if !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("filesystem: cleanup: %w", err)
	}
	return nil
}

// removeMatching removes the entries of directory dir whose name starts with prefix.
func removeMatching(r *os.Root, dir, prefix string) error {
	entries, err := fs.ReadDir(r.FS(), dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			errs = append(errs, r.RemoveAll(path.Join(dir, e.Name())))
		}
	}
	return errors.Join(errs...)
}
