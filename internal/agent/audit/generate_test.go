package audit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePackageSpec(t *testing.T) {
	p, err := ParsePackageSpec("nginx")
	if err != nil || p.Name != "nginx" || p.Version != "" {
		t.Errorf("plain: got %+v, %v", p, err)
	}
	p, err = ParsePackageSpec("nginx=1.24.0")
	if err != nil || p.Name != "nginx" || p.Version != "1.24.0" {
		t.Errorf("pinned: got %+v, %v", p, err)
	}
	if _, err := ParsePackageSpec("=1.0"); err == nil {
		t.Error("empty name: want error, got nil")
	}
	if _, err := ParsePackageSpec("nginx="); err == nil {
		t.Error("empty version: want error, got nil")
	}
}

func TestGenerateManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "watched.txt")
	if err := os.WriteFile(fpath, []byte("v1"), 0644); err != nil {
		t.Fatalf("write watched file: %v", err)
	}
	dest := filepath.Join(dir, "sub", "manifest.json")

	warnings, changed, err := GenerateManifest(dest, []string{fpath, filepath.Join(dir, "missing.txt")}, nil, nil, false)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !changed {
		t.Error("first write: want changed=true")
	}
	if len(warnings) != 1 {
		t.Errorf("warnings: got %v, want 1 missing-file warning", warnings)
	}

	m, err := LoadFile(dest)
	if err != nil {
		t.Fatalf("load generated: %v", err)
	}
	if len(m.Files) != 1 || m.Files[0].Path != fpath || m.Files[0].SHA256 == "" {
		t.Errorf("files: got %+v", m.Files)
	}

	_, changed, err = GenerateManifest(dest, []string{fpath, filepath.Join(dir, "missing.txt")}, nil, nil, false)
	if err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if changed {
		t.Error("identical regenerate: want changed=false")
	}

	// Tamper the watched file: CheckLocal must now report drift.
	if err := os.WriteFile(fpath, []byte("v2"), 0644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	p, ok := CheckLocal(dest, "gen-test")
	if !ok {
		t.Fatal("CheckLocal: want ok=true")
	}
	if p.Status != "DRIFT_DETECTED" {
		t.Errorf("status: got %q, want DRIFT_DETECTED", p.Status)
	}
}
