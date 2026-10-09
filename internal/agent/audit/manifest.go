package audit

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/assert"
)

// Check status values emitted for every manifest verification.
const (
	StatusOK               = "OK"
	StatusDriftDetected    = "DRIFT_DETECTED"
	StatusManifestNotFound = "MANIFEST_NOT_FOUND"
	StatusCheckError       = "CHECK_ERROR"
)

// ManifestStaleDays warns when the manifest has not been regenerated
// within this window. The check is advisory (stderr); it never changes
// the OK/DRIFT verdict.
const ManifestStaleDays = 90

// Remediation summarizes what one manifest-enforcement pass changed.
// Auto-remediation is NEVER on by default: it only runs when explicitly
// authorized (-remediate / remediate=true). Files are intentionally not in
// the agent's remediation scope — restoring file content is the provisioning
// playbook's job (IaS is the source of truth for file content).
type Remediation struct {
	PackagesRemoved  []string
	ServicesStarted  []string
	ServicesStopped  []string
	ServicesEnabled  []string
	ServicesDisabled []string
}

// Empty reports whether the remediation pass made no changes.
func (r Remediation) Empty() bool {
	return len(r.PackagesRemoved) == 0 &&
		len(r.ServicesStarted) == 0 &&
		len(r.ServicesStopped) == 0 &&
		len(r.ServicesEnabled) == 0 &&
		len(r.ServicesDisabled) == 0
}

// String renders the remediation as a single human-readable line.
func (r Remediation) String() string {
	var parts []string
	if n := len(r.PackagesRemoved); n > 0 {
		parts = append(parts, fmt.Sprintf("removed %d package(s): %s", n, strings.Join(r.PackagesRemoved, ", ")))
	}
	if n := len(r.ServicesStarted); n > 0 {
		parts = append(parts, fmt.Sprintf("started %d service(s): %s", n, strings.Join(r.ServicesStarted, ", ")))
	}
	if n := len(r.ServicesStopped); n > 0 {
		parts = append(parts, fmt.Sprintf("stopped %d service(s): %s", n, strings.Join(r.ServicesStopped, ", ")))
	}
	if n := len(r.ServicesEnabled); n > 0 {
		parts = append(parts, fmt.Sprintf("enabled %d service(s): %s", n, strings.Join(r.ServicesEnabled, ", ")))
	}
	if n := len(r.ServicesDisabled); n > 0 {
		parts = append(parts, fmt.Sprintf("disabled %d service(s): %s", n, strings.Join(r.ServicesDisabled, ", ")))
	}
	return strings.Join(parts, "; ")
}

// unauthorizedSet returns the elements of current that are not in allowed.
func unauthorizedSet(allowed, current []string) []string {
	allow := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allow[a] = true
	}
	var out []string
	for _, c := range current {
		if !allow[c] {
			out = append(out, c)
		}
	}
	return out
}

// RemediatePath loads the manifest at path and enforces it (see Remediate).
// Only meaningful when the caller has explicitly authorized remediation.
func RemediatePath(path string) (Remediation, error) {
	assert.True(path != "", "path must not be empty")
	m, err := loadManifest(path)
	if err != nil {
		return Remediation{}, err
	}
	return Remediate(m), nil
}

const secsPerDay = 86400

// CheckResult is the verdict of one manifest verification.
type CheckResult struct {
	NodeID              string
	TimestampUnix       int64
	Status              string
	Deviations          []string
	ManifestGeneratedAt int64
}

// Timestamp returns the check time as a time.Time.
func (r CheckResult) Timestamp() time.Time {
	return time.Unix(r.TimestampUnix, 0)
}

// ManifestAgeDays returns the whole-day age of a manifest generation timestamp.
func ManifestAgeDays(nowUnix, generatedAt int64) int64 {
	if generatedAt <= 0 {
		return 0
	}
	return (nowUnix - generatedAt) / secsPerDay
}

// IsManifestStale reports whether the manifest is older than ManifestStaleDays.
func IsManifestStale(nowUnix, generatedAt int64) bool {
	return ManifestAgeDays(nowUnix, generatedAt) > ManifestStaleDays
}

// ManifestFile is one file entry in the manifest.
type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Mode   string `json:"mode,omitempty"`
	Size   int64  `json:"size,omitempty"`  // bytes at record time
	MTime  int64  `json:"mtime,omitempty"` // unix seconds at record time
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
// and returns a CheckResult. Returns (result, false) if manifest path is empty.
func RunManifest(cfg *agentcfg.AgentConfig) (CheckResult, bool) {
	assert.True(cfg != nil, "cfg must not be nil")
	assert.True(cfg.NodeID != "", "node_id must not be empty")

	if cfg.ManifestPath == "" {
		return CheckResult{}, false
	}

	result := CheckResult{
		NodeID:        cfg.NodeID,
		TimestampUnix: time.Now().Unix(),
	}

	m, err := loadManifest(cfg.ManifestPath)
	if err != nil {
		result.Status = StatusManifestNotFound
		return result, true
	}

	if m.GeneratedAt > 0 && IsManifestStale(time.Now().Unix(), m.GeneratedAt) {
		age_days := ManifestAgeDays(time.Now().Unix(), m.GeneratedAt)
		fmt.Printf("audit: manifest is %d days old — re-run provisioning playbook to refresh\n", age_days)
	}

	result.ManifestGeneratedAt = m.GeneratedAt

	checks := cfg.CheckSet
	if len(checks) == 0 {
		checks = DefaultCheckSet()
	}

	var deviations []string
	deviations = append(deviations, checkFiles(m.Files, checks, cfg.FastScan)...)
	deviations = append(deviations, checkPackages(m.Packages)...)
	deviations = append(deviations, checkServices(m.Services)...)
	deviations = append(deviations, checkUnauthorizedPackages(m.PackageSnapshot)...)
	deviations = append(deviations, checkUnauthorizedServices(m.ServiceSnapshot)...)

	if len(deviations) > 0 {
		result.Status = StatusDriftDetected
		result.Deviations = deviations
	} else {
		result.Status = StatusOK
	}
	return result, true
}

// ProbeManifest verifies the manifest file exists and is parseable.
// Returns nil if manifest path is empty (manifest check disabled).
func ProbeManifest(cfg *agentcfg.AgentConfig) error {
	assert.True(cfg != nil, "cfg must not be nil")

	if cfg.ManifestPath == "" {
		return nil
	}
	_, err := loadManifest(cfg.ManifestPath)
	return err
}

func loadManifest(path string) (Manifest, error) {
	assert.True(path != "", "path must not be empty")

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

// checkFiles verifies every tracked file, hashing on a fixed bounded worker
// pool. Result order matches the manifest.
func checkFiles(files []ManifestFile, checks []string, fast bool) []string {
	type job struct {
		idx int
		f   ManifestFile
	}
	jobs := make(chan job)
	results := make([]error, len(files))

	var wg sync.WaitGroup
	for i := 0; i < boundedWorkers(len(files)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				results[j.idx] = checkFile(j.f, checks, fast)
			}
		}()
	}
	for i := range files {
		jobs <- job{i, files[i]}
	}
	close(jobs)
	wg.Wait()

	var deviations []string
	for i := range results {
		if results[i] != nil {
			deviations = append(deviations, fmt.Sprintf("file:%s:%s", results[i].Error(), files[i].Path))
		}
	}
	return deviations
}

// boundedWorkers caps the parallel hashing pool at the number of CPUs, clamped
// to the number of items (and at least one).
func boundedWorkers(items int) int {
	w := runtime.GOMAXPROCS(0)
	if w < 1 {
		w = 1
	}
	if items < w {
		w = items
	}
	if w < 1 {
		w = 1
	}
	return w
}

// checkFile verifies a tracked file. sha256 is always compared unless the
// record trusts stat: with fast=true, a matching size and mtime short-circuit
// the hash entirely. mode, size, and mtime are compared only when enabled in
// the check set.
func checkFile(f ManifestFile, checks []string, fast bool) error {
	assert.True(f.Path != "", "file path must not be empty")
	assert.True(f.SHA256 != "", "file sha256 must not be empty")

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

	if checkEnabled(checks, "mode") && f.Mode != "" {
		actual := fmt.Sprintf("%o", info.Mode().Perm())
		if actual != f.Mode {
			return fmt.Errorf("mode_mismatch")
		}
	}

	if fast && f.Size != 0 && f.MTime != 0 &&
		info.Size() == f.Size && info.ModTime().Unix() == f.MTime {
		return nil // unchanged by size+mtime; skip the hash
	}

	got, err := sha256File(f.Path)
	if err != nil {
		return fmt.Errorf("unreadable")
	}
	if got != f.SHA256 {
		return fmt.Errorf("hash_mismatch")
	}

	if checkEnabled(checks, "size") && f.Size != 0 && info.Size() != f.Size {
		return fmt.Errorf("size_mismatch")
	}
	if checkEnabled(checks, "mtime") && f.MTime != 0 && info.ModTime().Unix() != f.MTime {
		return fmt.Errorf("mtime_mismatch")
	}

	return nil
}

// DefaultCheckSet is the set of file attributes compared when the config does
// not specify one.
func DefaultCheckSet() []string { return []string{"sha256", "mode"} }

// checkEnabled reports whether name is in the check set.
func checkEnabled(checks []string, name string) bool {
	for _, c := range checks {
		if c == name {
			return true
		}
	}
	return false
}

func sha256File(path string) (string, error) {
	assert.True(path != "", "path must not be empty")

	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()

	h := sha256.New()
	// Read manually against the pooled buffer: io.CopyBuffer would dispatch to
	// os.File.WriteTo, which allocates its own 32KiB buffer per file.
	buf := hashBufPool.Get().(*[]byte)
	defer hashBufPool.Put(buf)
	for {
		n, err := fh.Read(*buf)
		if n > 0 {
			h.Write((*buf)[:n])
		}
		if err != nil {
			if err == io.EOF {
				return fmt.Sprintf("%x", h.Sum(nil)), nil
			}
			return "", err
		}
	}
}

// hashBufPool holds the read buffer reused across hashed files.
var hashBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 32*1024)
		return &b
	},
}
