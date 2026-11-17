package filesystem

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const id = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"

func TestServerDirsLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "servers")
	d := NewServerDirs(root, os.Getuid(), os.Getgid())
	d.now = func() time.Time { return time.Unix(100, 0) }

	path, err := d.Create(id)
	if err != nil || path != filepath.Join(root, id) {
		t.Fatalf("Create = %q, %v", path, err)
	}
	if _, err := d.Create(id); err == nil {
		t.Fatal("creating an existing directory must fail")
	}
	if err := os.WriteFile(filepath.Join(path, "level.dat"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Archive(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".deleted", id+"-100", "level.dat")); err != nil {
		t.Fatalf("archived file: %v", err)
	}
	if err := d.Archive(id); err != nil {
		t.Fatalf("archiving a missing directory: %v", err)
	}

	if _, err := d.Create(id); err != nil {
		t.Fatal(err)
	}
	if err := d.Remove(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("after Remove: %v", err)
	}
	if err := d.Remove(id); err != nil {
		t.Fatalf("removing a missing directory: %v", err)
	}
}

func TestServerDirsRejectInvalidIDs(t *testing.T) {
	d := NewServerDirs(t.TempDir(), os.Getuid(), os.Getgid())
	for _, bad := range []string{"", "..", "../etc", "abcdefghijklmnopqrstuvwxyz", id + "/x"} {
		if _, err := d.Create(bad); err == nil {
			t.Errorf("Create(%q) succeeded", bad)
		}
		if err := d.Remove(bad); err == nil {
			t.Errorf("Remove(%q) succeeded", bad)
		}
		if err := d.Archive(bad); err == nil {
			t.Errorf("Archive(%q) succeeded", bad)
		}
	}
}

func TestServerDirsRemoveDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	// The server directory itself is a symlink pointing outside root.
	if err := os.Symlink(outside, filepath.Join(root, id)); err != nil {
		t.Fatal(err)
	}
	if err := NewServerDirs(root, os.Getuid(), os.Getgid()).Remove(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("file outside root was touched: %v", err)
	}
}
