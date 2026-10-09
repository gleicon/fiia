package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTempFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if mtime.IsZero() {
		mtime = time.Now()
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFileIndex(t *testing.T) {
	dir := t.TempDir()
	writeTempFile(t, filepath.Join(dir, "a.conf"), "a=1\n", time.Time{})
	writeTempFile(t, filepath.Join(dir, "nested", "b.conf"), "b=2\n", time.Time{})

	idx, err := BuildFileIndex([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 {
		t.Fatalf("expected 2 entries, got %d: %v", len(idx), idx)
	}
	for path, attrs := range idx {
		if len(attrs.SHA256) != 64 {
			t.Fatalf("entry %s: bad sha256 %q", path, attrs.SHA256)
		}
		if attrs.Size <= 0 || attrs.MTime == 0 || attrs.Mode == "" {
			t.Fatalf("entry %s: missing attrs %+v", path, attrs)
		}
	}
}

func TestDiffIndexes(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, content string) {
		writeTempFile(t, filepath.Join(dir, name), content, time.Time{})
	}
	mk("a.conf", "a=1\n")
	mk("gone.conf", "gone\n")

	before, err := BuildFileIndex([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	// a.conf changed, b.conf added, gone.conf deleted
	writeTempFile(t, filepath.Join(dir, "a.conf"), "a=2\n", time.Time{})
	mk("b.conf", "b=3\n")
	if err := os.Remove(filepath.Join(dir, "gone.conf")); err != nil {
		t.Fatal(err)
	}
	after, err := BuildFileIndex([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	got := DiffIndexes(before, after)
	if len(got) != 3 {
		t.Fatalf("expected 3 changed (a changed, b added, gone deleted), got %v", got)
	}
	want := map[string]bool{"a.conf": true, "b.conf": true, "gone.conf": true}
	for _, p := range got {
		if !want[filepath.Base(p)] {
			t.Fatalf("unexpected changed path %q in %v", p, got)
		}
	}
}

func TestRecordBeginEnd(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "etc")
	index := filepath.Join(dir, "pre.json")
	manifest := filepath.Join(dir, "manifest.json")

	writeTempFile(t, filepath.Join(confDir, "a.conf"), "a=1\n", time.Time{})
	writeTempFile(t, filepath.Join(confDir, "c.conf"), "c=1\n", time.Time{})
	n, err := RecordBegin(index, []string{confDir})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("record-begin: expected 2 files, got %d", n)
	}

	// Cookbook run: change a.conf, add b.conf, delete c.conf.
	writeTempFile(t, filepath.Join(confDir, "a.conf"), "a=2\n", time.Time{})
	writeTempFile(t, filepath.Join(confDir, "b.conf"), "b=3\n", time.Time{})
	if err := os.Remove(filepath.Join(confDir, "c.conf")); err != nil {
		t.Fatal(err)
	}
	added, removed, err := RecordEnd(manifest, index, []string{confDir})
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 || removed != 1 {
		t.Fatalf("expected added=2 removed=1, got added=%d removed=%d", added, removed)
	}

	m, err := LoadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != 2 {
		t.Fatalf("expected 2 tracked files, got %d", len(m.Files))
	}
	paths := map[string]bool{}
	for _, f := range m.Files {
		paths[filepath.Base(f.Path)] = true
		if f.Size == 0 || f.MTime == 0 || f.Mode == "" {
			t.Fatalf("tracked file %s missing attrs: %+v", f.Path, f)
		}
	}
	if !paths["a.conf"] || !paths["b.conf"] {
		t.Fatalf("unexpected tracked set %v", paths)
	}

	// An idempotent re-run (fresh begin, no changes) produces no delta.
	if _, err := RecordBegin(index, []string{confDir}); err != nil {
		t.Fatal(err)
	}
	added2, removed2, err := RecordEnd(manifest, index, []string{confDir})
	if err != nil {
		t.Fatal(err)
	}
	if added2 != 0 || removed2 != 0 {
		t.Fatalf("idempotent re-run should add/remove nothing, got added=%d removed=%d", added2, removed2)
	}
}

func TestCheckFileSizeMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.conf")
	writeTempFile(t, path, "one\n", time.Time{})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := ManifestFile{
		Path:   path,
		SHA256: mustHash(t, path),
		Mode:   "644",
		Size:   info.Size(),
		MTime:  info.ModTime().Unix() - 5, // deliberately stale
	}

	// Default set ignores size/mtime.
	if err := checkFile(entry, nil, false); err != nil {
		t.Fatalf("default check set should pass with stale mtime: %v", err)
	}

	// Opting into size detects a size change.
	if err := os.WriteFile(path, []byte("one\nmore bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkFile(entry, []string{"sha256", "mode", "size", "mtime"}, false); err == nil {
		t.Fatal("expected size_mismatch to fail the check")
	}

	// Fast path trusts size+mtime and skips the hash entirely.
	writeTempFile(t, path, "one\n", time.Time{})
	unchanged, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entry2 := entry
	entry2.Size = unchanged.Size()
	entry2.MTime = unchanged.ModTime().Unix()
	if err := checkFile(entry2, nil, true); err != nil {
		t.Fatalf("fast path should pass on unchanged size+mtime: %v", err)
	}
	// A real change (content and size) is still caught, not trusted away.
	writeTempFile(t, path, "tampered\n", time.Now().Add(-10*time.Second))
	if err := checkFile(entry2, nil, true); err == nil {
		t.Fatal("fast path must still catch a real content change")
	}
}

func mustHash(t *testing.T, path string) string {
	t.Helper()
	sum, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	return sum
}
