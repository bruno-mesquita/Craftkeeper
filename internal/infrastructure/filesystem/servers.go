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

// ServerDirs manages the storage of each server: its data directory under root
// (<data dir>/servers) and its backups under backups (<data dir>/backups). Removal and
// archiving go through os.Root, so they cannot leave those directories even through symlinks.
type ServerDirs struct {
	root     string
	backups  string
	uid, gid int // owner of each server directory: the user the game container runs as
	now      func() time.Time
}

// NewServerDirs creates server directories owned by uid:gid, the user the game containers
// run as (it must be able to write its /data).
func NewServerDirs(root, backups string, uid, gid int) *ServerDirs {
	return &ServerDirs{root: root, backups: backups, uid: uid, gid: gid, now: time.Now}
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

// Remove deletes a server's data directory and its backups. Missing ones are not an error.
func (d *ServerDirs) Remove(id string) error {
	for _, root := range []string{d.root, d.backups} {
		if err := d.withRoot(root, id, func(r *os.Root) error { return r.RemoveAll(id) }); err != nil {
			return err
		}
	}
	return nil
}

// Archive moves a server's data directory and its backups to .deleted/<id>-<unix time> in
// their respective roots, out of the way but recoverable by hand. Missing ones are skipped.
func (d *ServerDirs) Archive(id string) error {
	stamp := id + "-" + strconv.FormatInt(d.now().Unix(), 10)
	for _, root := range []string{d.root, d.backups} {
		err := d.withRoot(root, id, func(r *os.Root) error {
			if _, err := r.Lstat(id); errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err := r.Mkdir(deletedDir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			return r.Rename(id, filepath.Join(deletedDir, stamp))
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *ServerDirs) withRoot(root, id string, fn func(*os.Root) error) error {
	if !domain.ValidServerID(id) {
		return fmt.Errorf("filesystem: invalid server id %q", id)
	}
	r, err := os.OpenRoot(root)
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
