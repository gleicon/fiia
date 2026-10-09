package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gleicon/fiia/internal/agent/audit"
)

// makeManifest writes a baseline sentinel and records a manifest tracking it.
func makeManifest(t *testing.T) (manifest, sentinel string) {
	t.Helper()
	dir := t.TempDir()
	manifest = filepath.Join(dir, "manifest.json")
	sentinel = filepath.Join(dir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := audit.GenerateManifest(manifest, []string{sentinel}, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	return manifest, sentinel
}

func TestRunCheckExitCodes(t *testing.T) {
	manifest, sentinel := makeManifest(t)

	if got := runCheck("", manifest, "text", "test-node", "", false); got != 0 {
		t.Fatalf("clean manifest: expected exit 0, got %d", got)
	}

	if err := os.WriteFile(sentinel, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runCheck("", manifest, "text", "test-node", "", false); got != 1 {
		t.Fatalf("drifted manifest: expected exit 1, got %d", got)
	}
}

func TestRunCheckUsageErrors(t *testing.T) {
	if got := runCheck("", "unused", "xml", "n", "", false); got != 2 {
		t.Fatalf("invalid format: expected exit 2, got %d", got)
	}

	missing := filepath.Join(t.TempDir(), "nope.json")
	if got := runCheck("", missing, "text", "n", "", false); got != 2 {
		t.Fatalf("missing manifest: expected exit 2, got %d", got)
	}

	// Config mode with no manifest in the config file.
	cfgMissing := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(cfgMissing, []byte("[agent]\nmanifest_path = \"/does/not/exist.json\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(cfgMissing, "", "text", "n", "", false); got != 2 {
		t.Fatalf("config-mode missing manifest: expected exit 2, got %d", got)
	}
}

func TestRunCheckConfigModeResolution(t *testing.T) {
	manifest, _ := makeManifest(t)
	cfg := filepath.Join(t.TempDir(), "agent.toml")
	body := "[agent]\nnode_id = \"cfg-node\"\nmanifest_path = " + quote(manifest) + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// No -manifest: the config supplies manifest_path and node id.
	if got := runCheck(cfg, "", "text", "", "", false); got != 0 {
		t.Fatalf("config-mode clean manifest: expected exit 0, got %d", got)
	}

	// -manifest wins over the config path.
	other := filepath.Join(filepath.Dir(manifest), "other.json")
	if _, _, err := audit.GenerateManifest(other, nil, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	if got := runCheck(cfg, other, "text", "", "", false); got != 0 {
		t.Fatalf("flag-mode clean manifest: expected exit 0, got %d", got)
	}
}

func TestResolveCheckInputsRejectsMissingManifest(t *testing.T) {
	_, _, _, err := resolveCheckInputs("", "", "", "")
	if err == nil || !strings.Contains(err.Error(), "no manifest to check") {
		t.Fatalf("expected missing-manifest error, got %v", err)
	}
}

func quote(s string) string {
	return "\"" + strings.ReplaceAll(s, "\\", "\\\\") + "\""
}
