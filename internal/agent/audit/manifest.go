package audit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/wire"
)

const (
	// manifest_stale_days: warn when manifest has not been regenerated within this window.
	manifest_stale_days = 90
)

// ManifestFile is one file entry in the manifest.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode,omitempty"`
	Owner  string `json:"owner,omitempty"`
	Group  string `json:"group,omitempty"`
}

// ManifestPackage is one package entry in the manifest.
type ManifestPackage struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ManifestService is one service entry in the manifest.
type ManifestService struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	Enabled bool   `json:"enabled"`
}

// Manifest is the desired-state document written by the ansible module and read by the agent.
type Manifest struct {
	SchemaVersion   int               `json:"schema_version"`
	GeneratedAt     int64             `json:"generated_at"`
	Files           []ManifestFile    `json:"files"`
	Packages        []ManifestPackage `json:"packages"`
	Services        []ManifestService `json:"services"`
	PackageSnapshot []string          `json:"package_snapshot,omitempty"` // all packages at provision time (mode: snapshot)
	ServiceSnapshot []string          `json:"service_snapshot,omitempty"` // all active services at provision time (mode: snapshot)
}

// RunManifest reads the manifest at cfg.ManifestPath, checks live system state,
// and returns a DriftPayload. Returns (payload, false) if manifest path is empty.
func RunManifest(cfg *agentcfg.AgentConfig) (wire.DriftPayload, bool) {
	assert(cfg != nil, "cfg must not be nil")
	assert(cfg.NodeID != "", "node_id must not be empty")

	if cfg.ManifestPath == "" {
		return wire.DriftPayload{}, false
	}

	payload := wire.DriftPayload{
		NodeID:        cfg.NodeID,
		TimestampUnix: time.Now().Unix(),
	}

	m, err := loadManifest(cfg.ManifestPath)
	if err != nil {
		payload.Status = "MANIFEST_NOT_FOUND"
		return payload, true
	}

	if m.GeneratedAt > 0 {
		age_days := (time.Now().Unix() - m.GeneratedAt) / 86400
		if age_days > manifest_stale_days {
			fmt.Printf("audit: manifest is %d days old — re-run provisioning playbook to refresh\n", age_days)
		}
	}

	payload.ManifestGeneratedAt = m.GeneratedAt

	var deviations []string
	deviations = append(deviations, checkFiles(m.Files)...)
	deviations = append(deviations, checkPackages(m.Packages)...)
	deviations = append(deviations, checkServices(m.Services)...)
	deviations = append(deviations, checkUnauthorizedPackages(m.PackageSnapshot)...)
	deviations = append(deviations, checkUnauthorizedServices(m.ServiceSnapshot)...)

	if len(deviations) > 0 {
		payload.Status = "DRIFT_DETECTED"
		payload.TasksChanged = deviations
	} else {
		payload.Status = "OK"
	}
	return payload, true
}

// ProbeManifest verifies the manifest file exists and is parseable.
// Returns nil if manifest path is empty (manifest check disabled).
func ProbeManifest(cfg *agentcfg.AgentConfig) error {
	assert(cfg != nil, "cfg must not be nil")

	if cfg.ManifestPath == "" {
		return nil
	}
	_, err := loadManifest(cfg.ManifestPath)
	return err
}

func loadManifest(path string) (Manifest, error) {
	assert(path != "", "path must not be empty")

	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest %q: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest %q: %w", path, err)
	}
	if m.SchemaVersion != 1 {
		return Manifest{}, fmt.Errorf("unsupported manifest schema_version: %d", m.SchemaVersion)
	}
	return m, nil
}

func checkFiles(files []ManifestFile) []string {
	var deviations []string
	for _, f := range files {
		if err := checkFile(f); err != nil {
			deviations = append(deviations, fmt.Sprintf("file:%s:%s", err.Error(), f.Path))
		}
	}
	return deviations
}

func checkFile(f ManifestFile) error {
	assert(f.Path != "", "file path must not be empty")
	assert(f.SHA256 != "", "file sha256 must not be empty")

	info, err := os.Stat(f.Path)
	if os.IsNotExist(err) {
		return fmt.Errorf("missing")
	}
	if err != nil {
		return fmt.Errorf("stat")
	}
	if info.IsDir() {
		return fmt.Errorf("is_directory")
	}

	got, err := sha256File(f.Path)
	if err != nil {
		return fmt.Errorf("unreadable")
	}
	if got != f.SHA256 {
		return fmt.Errorf("hash_mismatch")
	}

	if f.Mode != "" {
		actual := fmt.Sprintf("%o", info.Mode().Perm())
		if actual != f.Mode {
			return fmt.Errorf("mode_mismatch")
		}
	}

	return nil
}

func sha256File(path string) (string, error) {
	assert(path != "", "path must not be empty")

	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()

	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
