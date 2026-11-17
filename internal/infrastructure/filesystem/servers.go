// Package filesystem implements the panel's access to server data directories.
package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bruno-mesquita/mesquita-panel/internal/domain"
)

const deletedDir = ".deleted"

// ServerDirs manages one directory per server under root (<data dir>/servers). Removal and
// archiving go through os.Root, so they cannot leave root even through symlinks.
type ServerDirs struct {
	root     string
	uid, gid int // owner of each server directory: the user the game container runs as
	now      func() time.Time
}

// NewServerDirs creates server directories owned by uid:gid, the user the game containers
// run as (it must be able to write its /data).
func NewServerDirs(root string, uid, gid int) *ServerDirs {
	return &ServerDirs{root: root, uid: uid, gid: gid, now: time.Now}
}

// Create makes the data directory of a server, owned by the container user, and returns its
// absolute host path.
func (d *ServerDirs) Create(id string) (string, error) {
	if !domain.ValidServerID(id) {
		return "", fmt.Errorf("filesystem: invalid server id %q", id)
	}
	if err := os.MkdirAll(d.root, 0o750); err != nil {
		return "", fmt.Errorf("filesystem: %w", err)
	}
	path := filepath.Join(d.root, id)
	if err := os.Mkdir(path, 0o750); err != nil {
		return "", fmt.Errorf("filesystem: %w", err)
	}
	// A no-op when the panel itself is the container user; needed when it runs as root.
	if err := os.Lchown(path, d.uid, d.gid); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("filesystem: %w", err)
	}
	return path, nil
}

// Remove deletes a server's data directory. A missing directory is not an error.
func (d *ServerDirs) Remove(id string) error {
	return d.withRoot(id, func(r *os.Root) error {
		return r.RemoveAll(id)
	})
}

// Archive moves a server's data directory to .deleted/<id>-<unix time>, out of the way of
// live servers but recoverable by hand. A missing directory is not an error.
func (d *ServerDirs) Archive(id string) error {
	return d.withRoot(id, func(r *os.Root) error {
		if _, err := r.Lstat(id); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err := r.Mkdir(deletedDir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		return r.Rename(id, filepath.Join(deletedDir, id+"-"+strconv.FormatInt(d.now().Unix(), 10)))
	})
}

func (d *ServerDirs) withRoot(id string, fn func(*os.Root) error) error {
	if !domain.ValidServerID(id) {
		return fmt.Errorf("filesystem: invalid server id %q", id)
	}
	r, err := os.OpenRoot(d.root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("filesystem: %w", err)
	}
	defer r.Close()
	if err := fn(r); err != nil {
		return fmt.Errorf("filesystem: %w", err)
	}
	return nil
}
