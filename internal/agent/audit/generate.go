package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gleicon/fiia/internal/assert"
)

// PackageSpec declares one package to record in a generated manifest.
// Version is optional: when empty, the installed version is recorded.
type PackageSpec struct {
	Name    string
	Version string
}

// ParsePackageSpec parses "name" or "name=version".
func ParsePackageSpec(s string) (PackageSpec, error) {
	assert.True(s != "", "spec must not be empty")
	if name, ver, ok := strings.Cut(s, "="); ok {
		if name == "" || ver == "" {
			return PackageSpec{}, fmt.Errorf("bad package spec %q: want name or name=version", s)
		}
		return PackageSpec{Name: name, Version: ver}, nil
	}
	return PackageSpec{Name: s}, nil
}

// GenerateManifest builds a manifest from live system state and writes it to dest.
// This is the single canonical manifest generator: Ansible invokes it via
// -write-manifest (the fiia.fleet.agent role does this as its last step), and
// the agent reads the same file at audit time.
// Warnings are non-fatal (missing packages, unreadable files); changed reports
// whether content differs from any existing manifest (ignoring generated_at).
func GenerateManifest(dest string, files []string, pkgs []PackageSpec, svcs []string, snapshot bool) (warnings []string, changed bool, err error) {
	assert.True(dest != "", "dest must not be empty")

	m := Manifest{
		SchemaVersion: 1,
		GeneratedAt:   time.Now().Unix(),
	}

	warnings = append(warnings, collectFiles(files, &m)...)
	warnings = append(warnings, collectPackages(pkgs, &m)...)
	warnings = append(warnings, collectServices(svcs, &m)...)

	if snapshot {
		m.PackageSnapshot = ListInstalledPackages()
		sort.Strings(m.PackageSnapshot)
		m.ServiceSnapshot = ListActiveServices()
		sort.Strings(m.ServiceSnapshot)
	}

	existing, read_err := loadManifest(dest)
	if read_err != nil {
		changed = true // no usable manifest yet
	} else {
		changed = !manifestsEqual(existing, m)
	}

	if err := writeManifest(dest, m); err != nil {
		return warnings, changed, err
	}
	return warnings, changed, nil
}

// writeManifest writes the manifest atomically, owner-only readable.
func writeManifest(dest string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create manifest dir: %w", err)
		}
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0400); err != nil {
		return fmt.Errorf("write manifest tmp: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("install manifest: %w", err)
	}
	if err := os.Chmod(dest, 0400); err != nil {
		return fmt.Errorf("chmod manifest: %w", err)
	}
	return nil
}

// collectFiles hashes each declared file into the manifest, warning on any
// file that is missing or unreadable at provisioning time.
func collectFiles(files []string, m *Manifest) []string {
	var warnings []string
	for _, path := range files {
		info, stat_err := os.Stat(path)
		if stat_err != nil || !info.Mode().IsRegular() {
			warnings = append(warnings, fmt.Sprintf("file not found at provisioning time: %s", path))
			continue
		}
		sum, sum_err := sha256File(path)
		if sum_err != nil {
			warnings = append(warnings, fmt.Sprintf("file unreadable at provisioning time: %s", path))
			continue
		}
		m.Files = append(m.Files, ManifestFile{
			Path:   path,
			SHA256: sum,
			Mode:   fmt.Sprintf("%o", info.Mode().Perm()),
			Size:   info.Size(),
			MTime:  info.ModTime().Unix(),
		})
	}
	return warnings
}

// collectPackages records each declared package (installed version, or a
// pinned version), warning on packages not installed at provisioning time.
func collectPackages(pkgs []PackageSpec, m *Manifest) []string {
	var warnings []string
	for _, p := range pkgs {
		installed, ok := LookupPackageVersion(p.Name)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("package not installed at provisioning time: %s", p.Name))
		}
		entry := ManifestPackage{Name: p.Name}
		if v := firstNonEmpty(p.Version, installed); v != "" {
			entry.Version = v
		}
		m.Packages = append(m.Packages, entry)
	}
	return warnings
}

// collectServices records each declared service's actual state, warning when a
// service is not active or not enabled at provisioning time.
func collectServices(svcs []string, m *Manifest) []string {
	var warnings []string
	for _, name := range svcs {
		running, enabled := QueryServiceStates(name)
		m.Services = append(m.Services, ManifestService{
			Name:    name,
			Running: running,
			Enabled: enabled,
		})
		if !running {
			warnings = append(warnings, fmt.Sprintf("service not active at provisioning time: %s", name))
		}
		if !enabled {
			warnings = append(warnings, fmt.Sprintf("service not enabled at provisioning time: %s", name))
		}
	}
	return warnings
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// manifestsEqual compares two manifests ignoring generated_at.
func manifestsEqual(a, b Manifest) bool {
	a.GeneratedAt = 0
	b.GeneratedAt = 0
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}
