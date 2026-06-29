package audit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
)

func writeManifest(t *testing.T, dir string, m Manifest) string {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h[:])
}

func cfgWithManifest(path string) *agentcfg.AgentConfig {
	return &agentcfg.AgentConfig{
		NodeID:       "test-node",
		ManifestPath: path,
	}
}

func TestRunManifestDisabledWhenPathEmpty(t *testing.T) {
	cfg := &agentcfg.AgentConfig{NodeID: "n1", ManifestPath: ""}
	_, ok := RunManifest(cfg)
	if ok {
		t.Fatal("RunManifest should return ok=false when ManifestPath is empty")
	}
}

func TestRunManifestNotFound(t *testing.T) {
	cfg := cfgWithManifest("/does/not/exist/manifest.json")
	p, ok := RunManifest(cfg)
	if !ok {
		t.Fatal("RunManifest should return ok=true even when file is missing")
	}
	if p.Status != "MANIFEST_NOT_FOUND" {
		t.Errorf("status: got %q, want MANIFEST_NOT_FOUND", p.Status)
	}
}

func TestRunManifestCleanFile(t *testing.T) {
	dir := t.TempDir()
	content := []byte("hello fiia")
	file_path := filepath.Join(dir, "managed.conf")
	if err := os.WriteFile(file_path, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   1700000000,
		Files: []ManifestFile{
			{Path: file_path, SHA256: sha256Hex(content), Mode: "644"},
		},
	}
	cfg := cfgWithManifest(writeManifest(t, dir, m))
	p, ok := RunManifest(cfg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if p.Status != "OK" {
		t.Errorf("status: got %q, want OK; deviations: %v", p.Status, p.TasksChanged)
	}
	if len(p.TasksChanged) != 0 {
		t.Errorf("no deviations expected, got %v", p.TasksChanged)
	}
}

func TestRunManifestMissingFile(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   1700000000,
		Files: []ManifestFile{
			{Path: filepath.Join(dir, "ghost.conf"), SHA256: sha256Hex([]byte("x")), Mode: "644"},
		},
	}
	cfg := cfgWithManifest(writeManifest(t, dir, m))
	p, _ := RunManifest(cfg)
	if p.Status != "DRIFT_DETECTED" {
		t.Fatalf("status: got %q, want DRIFT_DETECTED", p.Status)
	}
	if len(p.TasksChanged) != 1 {
		t.Fatalf("deviation count: got %d, want 1", len(p.TasksChanged))
	}
	if p.TasksChanged[0][:len("file:missing:")] != "file:missing:" {
		t.Errorf("deviation prefix: got %q, want file:missing:...", p.TasksChanged[0])
	}
}

func TestRunManifestHashMismatch(t *testing.T) {
	dir := t.TempDir()
	file_path := filepath.Join(dir, "changed.conf")
	if err := os.WriteFile(file_path, []byte("actual content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   1700000000,
		Files: []ManifestFile{
			// Desired hash is for different content.
			{Path: file_path, SHA256: sha256Hex([]byte("expected content")), Mode: "644"},
		},
	}
	cfg := cfgWithManifest(writeManifest(t, dir, m))
	p, _ := RunManifest(cfg)
	if p.Status != "DRIFT_DETECTED" {
		t.Fatalf("status: got %q, want DRIFT_DETECTED", p.Status)
	}
	found := false
	for _, d := range p.TasksChanged {
		if len(d) > 14 && d[:14] == "file:hash_mism" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected hash_mismatch deviation, got %v", p.TasksChanged)
	}
}

func TestRunManifestModeMismatch(t *testing.T) {
	dir := t.TempDir()
	content := []byte("data")
	file_path := filepath.Join(dir, "mode.conf")
	if err := os.WriteFile(file_path, content, 0600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   1700000000,
		Files: []ManifestFile{
			{Path: file_path, SHA256: sha256Hex(content), Mode: "644"}, // expects 644, got 600
		},
	}
	cfg := cfgWithManifest(writeManifest(t, dir, m))
	p, _ := RunManifest(cfg)
	if p.Status != "DRIFT_DETECTED" {
		t.Fatalf("status: got %q, want DRIFT_DETECTED", p.Status)
	}
	found := false
	for _, d := range p.TasksChanged {
		if len(d) > 14 && d[:14] == "file:mode_mism" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected mode_mismatch deviation, got %v", p.TasksChanged)
	}
}

func TestRunManifestUnknownSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"schema_version":99,"generated_at":1700000000}`)
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	cfg := cfgWithManifest(path)
	p, ok := RunManifest(cfg)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if p.Status != "MANIFEST_NOT_FOUND" {
		t.Errorf("status: got %q, want MANIFEST_NOT_FOUND for bad schema", p.Status)
	}
}

func TestRunManifestMultipleDeviations(t *testing.T) {
	dir := t.TempDir()
	content := []byte("real")
	file_path := filepath.Join(dir, "real.conf")
	if err := os.WriteFile(file_path, content, 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	m := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   1700000000,
		Files: []ManifestFile{
			{Path: file_path, SHA256: sha256Hex(content), Mode: "644"},              // OK
			{Path: filepath.Join(dir, "missing.conf"), SHA256: sha256Hex([]byte("x")), Mode: "644"}, // missing
			{Path: file_path, SHA256: sha256Hex([]byte("wrong")), Mode: "644"},       // hash mismatch
		},
	}
	cfg := cfgWithManifest(writeManifest(t, dir, m))
	p, _ := RunManifest(cfg)
	if p.Status != "DRIFT_DETECTED" {
		t.Fatalf("status: got %q, want DRIFT_DETECTED", p.Status)
	}
	if len(p.TasksChanged) != 2 {
		t.Errorf("deviation count: got %d, want 2; deviations: %v", len(p.TasksChanged), p.TasksChanged)
	}
}

func TestProbeManifestEmptyPath(t *testing.T) {
	cfg := &agentcfg.AgentConfig{NodeID: "n1", ManifestPath: ""}
	if err := ProbeManifest(cfg); err != nil {
		t.Errorf("ProbeManifest with empty path should return nil, got %v", err)
	}
}

func TestProbeManifestValid(t *testing.T) {
	dir := t.TempDir()
	m := Manifest{SchemaVersion: 1, GeneratedAt: 1700000000}
	cfg := cfgWithManifest(writeManifest(t, dir, m))
	if err := ProbeManifest(cfg); err != nil {
		t.Errorf("ProbeManifest with valid manifest: %v", err)
	}
}
