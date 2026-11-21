package filesystem

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

const backupID = "BACKUPBACKUPBACKUPBACKUP22"

func newBackups(t *testing.T) (*Backups, string, string) {
	t.Helper()
	f, dir := newFiles(t)
	b := NewBackups(filepath.Join(t.TempDir(), "backups"), f)
	b.now = func() time.Time { return time.Unix(100, 0) }
	return b, dir, filepath.Dir(dir)
}

func readArchive(t *testing.T, b *Backups, bid string) map[string]string {
	t.Helper()
	f, size, err := b.Open(id, bid)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil || int64(len(raw)) != size {
		t.Fatalf("read archive: %d bytes of %d, %v", len(raw), size, err)
	}
	return zipNames(t, raw)
}

func TestBackupWrite(t *testing.T) {
	b, dir, _ := newBackups(t)
	write(t, filepath.Join(dir, "world", "level.dat"), "level")
	write(t, filepath.Join(dir, "server.properties"), "motd=hi")
	write(t, filepath.Join(dir, tempPrefix+"upload"), "partial")
	symlink(t, "/etc", filepath.Join(dir, "etc"))
	symlink(t, "../secret.txt", filepath.Join(dir, "secret"))

	size, err := b.Write(context.Background(), id, backupID)
	if err != nil || size <= 0 {
		t.Fatalf("Write = %d, %v", size, err)
	}
	got := readArchive(t, b, backupID)
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "server.properties,world/,world/level.dat" || got["world/level.dat"] != "level" {
		t.Fatalf("archive entries = %v", names)
	}
	noTempFiles(t, b.root)

	dirBytes, free, err := b.Usage(id)
	if err != nil || dirBytes != int64(len("level")+len("motd=hi")+len("partial")) || free <= 0 {
		t.Fatalf("Usage = %d, %d, %v", dirBytes, free, err)
	}

	if err := b.Remove(id, backupID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Open(id, backupID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Open after Remove = %v", err)
	}
	if err := b.Remove(id, backupID); err != nil {
		t.Fatalf("removing a missing archive: %v", err)
	}
}

func TestBackupWriteCancelled(t *testing.T) {
	b, dir, _ := newBackups(t)
	write(t, filepath.Join(dir, "level.dat"), strings.Repeat("x", 1<<20))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Write(ctx, id, backupID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write with a cancelled context = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(b.root, id))
	if err != nil || len(entries) != 0 {
		t.Fatalf("leftovers after a cancelled backup: %v, %v", entries, err)
	}
}

func TestBackupRejectsInvalidIDs(t *testing.T) {
	b, _, _ := newBackups(t)
	for _, bad := range [][2]string{{"..", backupID}, {id, "../x"}, {id, ""}, {"", backupID}} {
		if _, err := b.Write(context.Background(), bad[0], bad[1]); err == nil {
			t.Errorf("Write(%q, %q) succeeded", bad[0], bad[1])
		}
		if _, _, err := b.Open(bad[0], bad[1]); err == nil {
			t.Errorf("Open(%q, %q) succeeded", bad[0], bad[1])
		}
		if err := b.Remove(bad[0], bad[1]); err == nil {
			t.Errorf("Remove(%q, %q) succeeded", bad[0], bad[1])
		}
	}
	for _, staging := range []string{"", "..", id, restorePrefix + id + "-", restorePrefix + id + "-../x", restorePrefix + "OTHER-X"} {
		if err := b.CommitRestore(id, staging); err == nil {
			t.Errorf("CommitRestore(%q) succeeded", staging)
		}
		if err := b.DiscardRestore(id, staging); err == nil {
			t.Errorf("DiscardRestore(%q) succeeded", staging)
		}
	}
}

func TestBackupRestore(t *testing.T) {
	b, dir, serversRoot := newBackups(t)
	write(t, filepath.Join(dir, "world", "level.dat"), "original")
	if _, err := b.Write(context.Background(), id, backupID); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "world", "level.dat"), "broken")
	write(t, filepath.Join(dir, "added.txt"), "new")

	staging, err := b.StageRestore(context.Background(), id, backupID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "world", "level.dat")); string(got) != "broken" {
		t.Fatal("staging must not touch the live directory")
	}
	if err := b.CommitRestore(id, staging); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "world", "level.dat")); string(got) != "original" {
		t.Fatalf("restored level.dat = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "added.txt")); !os.IsNotExist(err) {
		t.Fatalf("added.txt after restore: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(serversRoot, deletedDir, id+"-100-pre-restore", "added.txt")); string(got) != "new" {
		t.Fatal("previous directory not kept in .deleted")
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != dirMode {
		t.Fatalf("restored directory: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(serversRoot, staging)); !os.IsNotExist(err) {
		t.Fatalf("staging directory after commit: %v", err)
	}

	// Discard leaves the live directory alone.
	staging, err = b.StageRestore(context.Background(), id, backupID, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.DiscardRestore(id, staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(serversRoot, staging)); !os.IsNotExist(err) {
		t.Fatalf("staging directory after discard: %v", err)
	}
}

func TestBackupRestoreFailures(t *testing.T) {
	b, dir, serversRoot := newBackups(t)
	write(t, filepath.Join(dir, "level.dat"), strings.Repeat("x", 4096))
	if _, err := b.Write(context.Background(), id, backupID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageRestore(context.Background(), id, backupID, 100); !errors.Is(err, domain.ErrInsufficientStorage) {
		t.Fatalf("restore past the byte budget = %v", err)
	}
	if _, err := b.StageRestore(context.Background(), id, "MISSINGMISSINGMISSINGMISS2", 1<<20); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("restore of a missing archive = %v", err)
	}

	// A tampered archive cannot write outside the staging directory.
	hostile := filepath.Join(b.root, id, "HOSTILEHOSTILEHOSTILEHOST2"+backupExt)
	if err := os.WriteFile(hostile, makeZip(t, zipEntry{name: "ok.txt", content: "x"}, zipEntry{name: "../escaped.txt", content: "x"}), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StageRestore(context.Background(), id, "HOSTILEHOSTILEHOSTILEHOST2", 1<<20); !errors.Is(err, domain.ErrInvalidArchive) {
		t.Fatalf("restore of a hostile archive = %v", err)
	}
	if _, err := os.Stat(filepath.Join(serversRoot, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("file escaped the staging directory: %v", err)
	}

	entries, _ := os.ReadDir(serversRoot)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), restorePrefix) {
			t.Errorf("staging directory left after a failure: %s", e.Name())
		}
	}
}

func TestBackupCleanupStale(t *testing.T) {
	b, dir, serversRoot := newBackups(t)
	write(t, filepath.Join(dir, "level.dat"), "x")
	if _, err := b.Write(context.Background(), id, backupID); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(serversRoot, restorePrefix+id+"-STALE", "level.dat"), "x")
	write(t, filepath.Join(b.root, id, tempPrefix+"STALE"), "partial")
	write(t, filepath.Join(dir, domain.WorldStagingPrefix+"STALE", "level.dat"), "x")

	if err := b.CleanupStale(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(serversRoot, restorePrefix+id+"-STALE")); !os.IsNotExist(err) {
		t.Fatalf("stale staging directory: %v", err)
	}
	noTempFiles(t, b.root)
	noTempFiles(t, dir)
	if _, _, err := b.Open(id, backupID); err != nil {
		t.Fatalf("finished backups must survive cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "level.dat")); err != nil {
		t.Fatalf("server files must survive cleanup: %v", err)
	}
	if err := NewBackups(filepath.Join(t.TempDir(), "none"), b.files).CleanupStale(); err != nil {
		t.Fatalf("cleanup without a backups directory: %v", err)
	}
}
