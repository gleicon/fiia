package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckLocalClean(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello local")
	fpath := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(fpath, content, 0644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}
	mpath := writeManifest(t, dir, Manifest{
		SchemaVersion: 1,
		GeneratedAt:   9999999999,
		Files:         []ManifestFile{{Path: fpath, SHA256: sha256Hex(content)}},
	})

	p, ok := CheckLocal(mpath, "local-test")
	if !ok {
		t.Fatal("CheckLocal: want ok=true")
	}
	if p.Status != "OK" {
		t.Errorf("status: got %q, want OK", p.Status)
	}
}

func TestCheckLocalDrift(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(fpath, []byte("v1"), 0644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}
	mpath := writeManifest(t, dir, Manifest{
		SchemaVersion: 1,
		Files:         []ManifestFile{{Path: fpath, SHA256: sha256Hex([]byte("v1"))}},
	})
	if err := os.WriteFile(fpath, []byte("v2"), 0644); err != nil {
		t.Fatalf("tamper watched file: %v", err)
	}

	p, ok := CheckLocal(mpath, "local-test")
	if !ok {
		t.Fatal("CheckLocal: want ok=true")
	}
	if p.Status != "DRIFT_DETECTED" {
		t.Errorf("status: got %q, want DRIFT_DETECTED", p.Status)
	}
	if len(p.Deviations) != 1 {
		t.Errorf("deviations: got %d, want 1", len(p.Deviations))
	}
}

func TestCheckLocalMissingManifest(t *testing.T) {
	p, ok := CheckLocal(filepath.Join(t.TempDir(), "nope.json"), "local-test")
	if !ok {
		t.Fatal("CheckLocal: want ok=true even when file is missing")
	}
	if p.Status != "MANIFEST_NOT_FOUND" {
		t.Errorf("status: got %q, want MANIFEST_NOT_FOUND", p.Status)
	}
}

func TestLoadFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	mpath := writeManifest(t, dir, Manifest{SchemaVersion: 1, GeneratedAt: 42})
	m, err := LoadFile(mpath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if m.GeneratedAt != 42 {
		t.Errorf("generated_at: got %d, want 42", m.GeneratedAt)
	}
	if _, err := LoadFile(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("LoadFile missing: want error, got nil")
	}
}
