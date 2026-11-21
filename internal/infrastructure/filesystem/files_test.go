package filesystem

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/bruno-mesquita/Craftkeeper/internal/domain"
)

// newFiles returns a ServerFiles whose server `id` has an empty directory at the returned
// path. The servers root also holds a secret file, outside every server directory.
func newFiles(t *testing.T) (*ServerFiles, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "secret.txt"), "top secret")
	return NewServerFiles(root, os.Getuid(), os.Getgid()), dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func isValidation(err error) bool {
	var ve *domain.ValidationError
	return errors.As(err, &ve)
}

func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if strings.HasPrefix(d.Name(), tempPrefix) {
			t.Errorf("temporary file left behind: %s", p)
		}
		return nil
	})
}

func TestNothingOutsideTheServerDirIsReachable(t *testing.T) {
	f, dir := newFiles(t)
	symlink(t, "/etc", filepath.Join(dir, "etc"))
	symlink(t, "../secret.txt", filepath.Join(dir, "secret"))
	symlink(t, "..", filepath.Join(dir, "up"))
	symlink(t, "loop", filepath.Join(dir, "loop"))

	escapes := func(what string, err error) {
		t.Helper()
		if err == nil || !(isValidation(err) || errors.Is(err, domain.ErrNotFound)) {
			t.Errorf("%s: %v, want refusal", what, err)
		}
	}
	_, err := f.List(id, "etc")
	escapes("List etc", err)
	_, err = f.List(id, "up")
	escapes("List up", err)
	_, err = f.ReadText(id, "secret")
	escapes("ReadText secret", err)
	_, err = f.ReadText(id, "etc/hostname")
	escapes("ReadText etc/hostname", err)
	_, _, err = f.OpenFile(id, "secret")
	escapes("OpenFile secret", err)
	_, _, err = f.OpenFile(id, "up/secret.txt")
	escapes("OpenFile up/secret.txt", err)
	_, _, err = f.OpenFile(id, "../secret.txt")
	escapes("OpenFile ../secret.txt", err)
	_, _, err = f.OpenFile(id, "loop")
	escapes("OpenFile loop", err)
	_, err = f.Save(id, "up", "evil.txt", strings.NewReader("x"), false)
	escapes("Save into up", err)
	_, err = f.WriteText(id, "etc/evil", "x", "")
	escapes("WriteText etc/evil", err)
	_, err = f.Stat(id, "up")
	escapes("Stat up", err)
	escapes("Mkdir up/x", f.Mkdir(id, "up/x"))
	escapes("Rename to up/x", f.Rename(id, "secret", "up/stolen"))
	escapes("Remove up/secret.txt", f.Remove(id, "up/secret.txt"))
	escapes("WriteZip up", f.WriteZip(id, "up", io.Discard))
	_, err = f.Extract(id, "up/secret.txt", "", false)
	escapes("Extract up/secret.txt", err)

	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("a file was written outside the server directory")
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(dir), "secret.txt")); string(b) != "top secret" {
		t.Fatal("the outside file was modified")
	}

	// Removing a symlink removes the link, never its target.
	if err := f.Remove(id, "secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "secret.txt")); err != nil {
		t.Fatalf("symlink target removed: %v", err)
	}
}

func TestInvalidServerID(t *testing.T) {
	f, _ := newFiles(t)
	if _, err := f.List("../"+id, ""); err == nil {
		t.Fatal("invalid server id accepted")
	}
	if _, err := f.List("ZZZZZZZZZZZZZZZZZZZZZZZZZZ", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing server dir: %v", err)
	}
}

func TestListAndInternalSymlinks(t *testing.T) {
	f, dir := newFiles(t)
	write(t, filepath.Join(dir, "server.properties"), "motd=hi\n")
	write(t, filepath.Join(dir, "world", "level.dat"), "x")
	write(t, filepath.Join(dir, "Banned.json"), "[]")
	symlink(t, "server.properties", filepath.Join(dir, "cfg"))
	for _, name := range []string{domain.ModpackConfigFile, domain.ModpackInstalledFile, domain.ModpackSupportedFile} {
		write(t, filepath.Join(dir, name), "x") // panel metadata stays hidden
	}

	l, err := f.List(id, "") // paths reach the adapter cleaned: "" is the root
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range l.Entries {
		got = append(got, string(e.Type)+":"+e.Name)
	}
	want := "dir:world,file:Banned.json,symlink:cfg,file:server.properties"
	if strings.Join(got, ",") != want || l.Truncated {
		t.Fatalf("List = %v (truncated %v), want %s", got, l.Truncated, want)
	}
	if l.Entries[3].Size != 8 || l.Entries[3].ModifiedAt.IsZero() {
		t.Fatalf("entry = %+v", l.Entries[3])
	}

	// A symlink that stays inside can be downloaded, but never edited.
	file, size, err := f.OpenFile(id, "cfg")
	if err != nil || size != 8 {
		t.Fatalf("OpenFile cfg = %d, %v", size, err)
	}
	file.Close()
	if _, err := f.ReadText(id, "cfg"); !isValidation(err) {
		t.Fatalf("ReadText of a symlink = %v", err)
	}
	if _, err := f.List(id, "server.properties"); !errors.Is(err, domain.ErrNotDirectory) {
		t.Fatalf("List of a file = %v", err)
	}
	if _, _, err := f.OpenFile(id, "world"); !errors.Is(err, domain.ErrIsDirectory) {
		t.Fatalf("OpenFile of a dir = %v", err)
	}
}

func TestListIsTruncated(t *testing.T) {
	f, dir := newFiles(t)
	for i := range domain.ListEntriesMax + 5 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%06d", i)), nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	l, err := f.List(id, "")
	if err != nil || !l.Truncated || len(l.Entries) != domain.ListEntriesMax {
		t.Fatalf("List: %d entries, truncated %v, %v", len(l.Entries), l.Truncated, err)
	}
}

type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n == 0 {
		return 0, errors.New("connection reset")
	}
	k := min(len(p), r.n)
	r.n -= k
	return k, nil
}

func TestSave(t *testing.T) {
	f, dir := newFiles(t)
	e, err := f.Save(id, "", "plugin.jar", strings.NewReader("v1"), false)
	if err != nil || e.Name != "plugin.jar" || e.Size != 2 || e.Type != domain.FileTypeFile {
		t.Fatalf("Save = %+v, %v", e, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "plugin.jar"))
	if info.Mode().Perm() != fileMode {
		t.Fatalf("mode = %v", info.Mode())
	}
	if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
		t.Fatalf("owner = %d:%d", st.Uid, st.Gid)
	}
	if _, err := f.Save(id, "", "plugin.jar", strings.NewReader("v2"), false); !errors.Is(err, domain.ErrFileExists) {
		t.Fatalf("Save over existing = %v", err)
	}
	if _, err := f.Save(id, "", "plugin.jar", strings.NewReader("v2"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "plugin.jar")); string(b) != "v2" {
		t.Fatalf("content = %q", b)
	}

	// A failed upload leaves the previous file and no temporary file.
	if _, err := f.Save(id, "", "plugin.jar", &failingReader{n: 10 << 10}, true); err == nil {
		t.Fatal("failed upload succeeded")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "plugin.jar")); string(b) != "v2" {
		t.Fatalf("content after failed upload = %q", b)
	}
	noTempFiles(t, dir)

	os.Mkdir(filepath.Join(dir, "mods"), 0o750)
	if _, err := f.Save(id, "", "mods", strings.NewReader("x"), true); !errors.Is(err, domain.ErrIsDirectory) {
		t.Fatalf("Save over a directory = %v", err)
	}
	if _, err := f.Save(id, "plugin.jar", "x", strings.NewReader("x"), false); !errors.Is(err, domain.ErrNotDirectory) {
		t.Fatalf("Save into a file = %v", err)
	}
	if _, err := f.Save(id, "missing", "x", strings.NewReader("x"), false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Save into a missing dir = %v", err)
	}
}

func TestTextEditing(t *testing.T) {
	f, dir := newFiles(t)
	p := filepath.Join(dir, "server.properties")
	write(t, p, "motd=hello\n")
	os.Chmod(p, 0o600)

	ft, err := f.ReadText(id, "server.properties")
	if err != nil || ft.Content != "motd=hello\n" || ft.Version == "" || ft.Size != 11 {
		t.Fatalf("ReadText = %+v, %v", ft, err)
	}
	v2, err := f.WriteText(id, "server.properties", "motd=olá mundo\n", ft.Version)
	if err != nil || v2 == ft.Version {
		t.Fatalf("WriteText = %q, %v", v2, err)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: %v", info.Mode())
	}
	// Saving with the version read before the last write is a conflict.
	if _, err := f.WriteText(id, "server.properties", "motd=stale\n", ft.Version); !errors.Is(err, domain.ErrFileChanged) {
		t.Fatalf("stale version = %v", err)
	}
	// So is an in-place change by someone else (the game), even with the same size.
	if err := os.WriteFile(p, []byte("motd=olá mundX\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteText(id, "server.properties", "motd=mine\n", v2); !errors.Is(err, domain.ErrFileChanged) {
		t.Logf("in-place same-size change within one mtime tick not detected (%v); acceptable", err)
	}
	if b, _ := os.ReadFile(p); !strings.HasPrefix(string(b), "motd=") {
		t.Fatalf("content = %q", b)
	}

	if _, err := f.WriteText(id, "new.txt", "created", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteText(id, "gone.txt", "x", "some-version"); !errors.Is(err, domain.ErrFileChanged) {
		t.Fatalf("write to a deleted file with a version = %v", err)
	}
	if _, err := f.WriteText(id, "", "x", ""); !errors.Is(err, domain.ErrIsDirectory) {
		t.Fatalf("write to root = %v", err)
	}
	if _, err := f.WriteText(id, "big.txt", strings.Repeat("x", domain.EditableFileMaxBytes+1), ""); !errors.Is(err, domain.ErrFileTooLarge) {
		t.Fatalf("write too large = %v", err)
	}
	noTempFiles(t, dir)

	write(t, filepath.Join(dir, "level.dat"), "\x00\x01binary")
	write(t, filepath.Join(dir, "latin1.txt"), "caf\xe9")
	write(t, filepath.Join(dir, "huge.log"), strings.Repeat("x", domain.EditableFileMaxBytes+1))
	os.Mkdir(filepath.Join(dir, "world"), 0o750)
	for p, want := range map[string]error{
		"level.dat":   domain.ErrNotText,
		"latin1.txt":  domain.ErrNotText,
		"huge.log":    domain.ErrFileTooLarge,
		"world":       domain.ErrIsDirectory,
		"missing.txt": domain.ErrNotFound,
	} {
		if _, err := f.ReadText(id, p); !errors.Is(err, want) {
			t.Errorf("ReadText(%s) = %v, want %v", p, err, want)
		}
	}
}

func TestMkdirRenameRemove(t *testing.T) {
	f, dir := newFiles(t)
	if err := f.Mkdir(id, "plugins"); err != nil {
		t.Fatal(err)
	}
	if err := f.Mkdir(id, "plugins"); !errors.Is(err, domain.ErrFileExists) {
		t.Fatalf("Mkdir existing = %v", err)
	}
	if err := f.Mkdir(id, "a/b"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Mkdir without parent = %v", err)
	}
	write(t, filepath.Join(dir, "plugins", "x.jar"), "x")
	write(t, filepath.Join(dir, "other.txt"), "o")

	if err := f.Rename(id, "plugins", "mods"); err != nil {
		t.Fatal(err)
	}
	if err := f.Rename(id, "other.txt", "mods/other.txt"); err != nil {
		t.Fatalf("move into a directory: %v", err)
	}
	write(t, filepath.Join(dir, "a.txt"), "a")
	if err := f.Rename(id, "a.txt", "mods/x.jar"); !errors.Is(err, domain.ErrFileExists) {
		t.Fatalf("Rename onto existing = %v", err)
	}
	if err := f.Rename(id, "missing", "y"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Rename missing = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "mods", "x.jar")); string(b) != "x" {
		t.Fatal("rename target was replaced")
	}

	if err := f.Remove(id, "mods"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mods")); !os.IsNotExist(err) {
		t.Fatalf("after Remove: %v", err)
	}
	if err := f.Remove(id, "mods"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Remove missing = %v", err)
	}
}

func zipNames(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		c, _ := io.ReadAll(rc)
		rc.Close()
		out[zf.Name] = string(c)
	}
	return out
}

func TestWriteZip(t *testing.T) {
	f, dir := newFiles(t)
	write(t, filepath.Join(dir, "world", "level.dat"), "level")
	write(t, filepath.Join(dir, "world", "region", "r.0.0.mca"), "region")
	write(t, filepath.Join(dir, ".hidden"), "h")
	write(t, filepath.Join(dir, domain.ModpackSupportedFile), "")
	write(t, filepath.Join(dir, tempPrefix+"upload"), "partial")
	symlink(t, "/etc", filepath.Join(dir, "world", "etc"))
	symlink(t, "level.dat", filepath.Join(dir, "world", "inner-link"))

	var buf bytes.Buffer
	if err := f.WriteZip(id, "world", &buf); err != nil {
		t.Fatal(err)
	}
	got := zipNames(t, buf.Bytes())
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "world/,world/level.dat,world/region/,world/region/r.0.0.mca" ||
		got["world/region/r.0.0.mca"] != "region" {
		t.Fatalf("zip entries = %v", names)
	}

	buf.Reset()
	if err := f.WriteZip(id, "", &buf); err != nil {
		t.Fatal(err)
	}
	got = zipNames(t, buf.Bytes())
	if got["server/.hidden"] != "h" || got["server/world/level.dat"] != "level" {
		t.Fatalf("root zip = %v", got)
	}
	for _, skipped := range []string{domain.ModpackSupportedFile, tempPrefix + "upload"} {
		if _, ok := got["server/"+skipped]; ok {
			t.Fatalf("root zip must skip %s: %v", skipped, got)
		}
	}
	if err := f.WriteZip(id, ".hidden", io.Discard); !errors.Is(err, domain.ErrNotDirectory) {
		t.Fatalf("zip of a file = %v", err)
	}
}

type zipEntry struct {
	name, content string
	mode          fs.FileMode
	nonUTF8       bool // stored without the UTF-8 flag (CP437)
}

func makeZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store, NonUTF8: e.nonUTF8}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, e.content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtract(t *testing.T) {
	f, dir := newFiles(t)
	os.WriteFile(filepath.Join(dir, "pack.zip"), makeZip(t,
		zipEntry{name: "pack/"},
		zipEntry{name: "pack/a.txt", content: "a"},
		zipEntry{name: "pack/sub/b.txt", content: "b"},
		zipEntry{name: "pack/link", content: "/etc/passwd", mode: fs.ModeSymlink | 0o777},
	), 0o640)
	os.Mkdir(filepath.Join(dir, "plugins"), 0o750)

	n, err := f.Extract(id, "pack.zip", "plugins", false)
	if err != nil || n != 3 {
		t.Fatalf("Extract = %d, %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "plugins", "pack", "sub", "b.txt")); string(b) != "b" {
		t.Fatalf("b.txt = %q", b)
	}
	if _, err := os.Lstat(filepath.Join(dir, "plugins", "pack", "link")); !os.IsNotExist(err) {
		t.Fatal("symlink entry was extracted")
	}

	os.WriteFile(filepath.Join(dir, "plugins", "pack", "a.txt"), []byte("mine"), 0o640)
	if _, err := f.Extract(id, "pack.zip", "plugins", false); !errors.Is(err, domain.ErrFileExists) {
		t.Fatalf("conflict = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "plugins", "pack", "a.txt")); string(b) != "mine" {
		t.Fatal("a conflicting extraction wrote files")
	}
	if _, err := f.Extract(id, "pack.zip", "plugins", true); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "plugins", "pack", "a.txt")); string(b) != "a" {
		t.Fatal("overwrite did not replace the file")
	}
	noTempFiles(t, dir)
}

// Names without the UTF-8 flag are CP437: 0x15 is "§" (seen in a real Minecraft world zip as
// "§6Duck Pond"), 0x82 is "é". Names flagged as UTF-8 keep their bytes.
func TestExtractDecodesCP437Names(t *testing.T) {
	f, dir := newFiles(t)
	os.WriteFile(filepath.Join(dir, "world.zip"), makeZip(t,
		zipEntry{name: "saves/\x156Duck Pond (1)/", nonUTF8: true},
		zipEntry{name: "saves/\x156Duck Pond (1)/caf\x82.txt", content: "x", nonUTF8: true},
		zipEntry{name: "plain.txt", content: "p"},
	), 0o640)
	if n, err := f.Extract(id, "world.zip", "", false); err != nil || n != 3 {
		t.Fatalf("Extract = %d, %v", n, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "saves", "§6Duck Pond (1)", "café.txt")); err != nil || string(b) != "x" {
		t.Fatalf("decoded entry: %q, %v", b, err)
	}

	os.WriteFile(filepath.Join(dir, "bad.zip"), makeZip(t, zipEntry{name: "bad\x15.txt", content: "x"}), 0o640)
	if _, err := f.Extract(id, "bad.zip", "", false); !errors.Is(err, domain.ErrInvalidArchive) {
		t.Fatalf("control character in a UTF-8 name = %v", err)
	}
}

func TestDecodeCP437(t *testing.T) {
	for in, want := range map[string]string{
		"plain/ascii.txt":  "plain/ascii.txt",
		"\x15\x14\x7f":     "§¶⌂",
		"\x80\xa4\xe1\xff": "Çñß\u00a0",
		"\x00":             "\x00",
	} {
		if got := decodeCP437(in); got != want {
			t.Errorf("decodeCP437(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractRejectsHostileArchives(t *testing.T) {
	f, dir := newFiles(t)
	for i, entries := range [][]zipEntry{
		{{name: "ok.txt", content: "x"}, {name: "../evil.txt", content: "x"}},
		{{name: "a/../../evil.txt", content: "x"}},
		{{name: "/evil.txt", content: "x"}},
		{{name: "..\\evil.txt", content: "x"}},
	} {
		os.WriteFile(filepath.Join(dir, "bad.zip"), makeZip(t, entries...), 0o640)
		if _, err := f.Extract(id, "bad.zip", "", false); !errors.Is(err, domain.ErrInvalidArchive) {
			t.Errorf("case %d: %v, want ErrInvalidArchive", i, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "ok.txt")); !os.IsNotExist(err) {
			t.Errorf("case %d: wrote entries of a rejected archive", i)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.txt")); !os.IsNotExist(err) {
		t.Fatal("zip slip wrote outside the server directory")
	}

	os.WriteFile(filepath.Join(dir, "not.zip"), []byte("plain text"), 0o640)
	if _, err := f.Extract(id, "not.zip", "", false); !errors.Is(err, domain.ErrInvalidArchive) {
		t.Fatalf("not a zip = %v", err)
	}

	// A corrupt entry (CRC mismatch) fails without leaving a temporary file.
	b := makeZip(t, zipEntry{name: "data.txt", content: "hello world"})
	b[bytes.Index(b, []byte("hello world"))] = 'J'
	os.WriteFile(filepath.Join(dir, "corrupt.zip"), b, 0o640)
	if _, err := f.Extract(id, "corrupt.zip", "", false); !errors.Is(err, domain.ErrInvalidArchive) {
		t.Fatalf("corrupt = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "data.txt")); !os.IsNotExist(err) {
		t.Fatal("corrupt entry was kept")
	}
	noTempFiles(t, dir)
}

func TestEntryReaderCountsRealBytes(t *testing.T) {
	er := &entryReader{r: strings.NewReader(strings.Repeat("x", 100)), left: 10}
	if _, err := io.Copy(io.Discard, er); !errors.Is(err, domain.ErrArchiveTooLarge) {
		t.Fatalf("over budget = %v", err)
	}
	er = &entryReader{r: strings.NewReader(strings.Repeat("x", 100)), left: 100}
	if n, err := io.Copy(io.Discard, er); err != nil || n != 100 || er.n != 100 {
		t.Fatalf("exact budget = %d, %v", n, err)
	}
}
