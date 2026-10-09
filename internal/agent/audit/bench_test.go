package audit

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// benchTree writes n small regular files into a temp dir.
func benchTree(b *testing.B, n int) string {
	b.Helper()
	dir := b.TempDir()
	payload := []byte(strings.Repeat("x", 1024))
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.conf", i)), payload, 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

// manifestFromIndex builds a fresh ManifestFile list from a built index.
func manifestFromIndex(b *testing.B, dir string) []ManifestFile {
	b.Helper()
	idx, err := BuildFileIndex([]string{dir})
	if err != nil {
		b.Fatal(err)
	}
	files := make([]ManifestFile, 0, len(idx))
	for path, attrs := range idx {
		files = append(files, attrs.toManifestFile(path))
	}
	return files
}

func BenchmarkBuildFileIndex(b *testing.B) {
	dir := benchTree(b, 2000)
	b.ReportAllocs()
	for b.Loop() {
		idx, err := BuildFileIndex([]string{dir})
		if err != nil {
			b.Fatal(err)
		}
		if len(idx) != 2000 {
			b.Fatalf("indexed %d files", len(idx))
		}
	}
}

func BenchmarkCheckFiles(b *testing.B) {
	dir := benchTree(b, 2000)
	files := manifestFromIndex(b, dir)
	b.ReportAllocs()
	for b.Loop() {
		devs := checkFiles(files, DefaultCheckSet(), false)
		if len(devs) != 0 {
			b.Fatalf("unexpected deviations: %v", devs)
		}
	}
}

// BenchmarkCheckFilesFast measures the stat-only path: unchanged size+mtime
// skip the sha256 read entirely.
func BenchmarkCheckFilesFast(b *testing.B) {
	dir := benchTree(b, 2000)
	files := manifestFromIndex(b, dir)
	b.ReportAllocs()
	for b.Loop() {
		devs := checkFiles(files, DefaultCheckSet(), true)
		if len(devs) != 0 {
			b.Fatalf("unexpected deviations: %v", devs)
		}
	}
}
