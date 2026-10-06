//go:build linux

package audit

import (
	"fmt"
	"github.com/gleicon/fiia/internal/assert"
	"os/exec"
	"strings"
)

// LookupPackageVersion returns the installed version of name, or ("", false)
// when the package is not installed. Used by manifest generation.
func LookupPackageVersion(name string) (string, bool) {
	v, err := queryPackageVersion(name)
	if err != nil {
		return "", false
	}
	return v, true
}

// ListInstalledPackages returns all installed package names (dpkg, rpm fallback).
// Returns nil when no package manager is present. Used by snapshot generation.
func ListInstalledPackages() []string {
	pkgs, err := listInstalledPackages()
	if err != nil {
		return nil
	}
	return pkgs
}

// ListActiveServices returns active service names (without .service suffix).
// Returns nil on non-systemd hosts. Used by snapshot generation.
func ListActiveServices() []string {
	svcs, err := listActiveServices()
	if err != nil {
		return nil
	}
	return svcs
}

// QueryServiceStates reports current running/enabled states via systemctl.
// Returns (false, false) on non-systemd hosts. Used by manifest generation,
// which records actual state at provisioning time.
func QueryServiceStates(name string) (running, enabled bool) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false, false
	}
	running = exec.Command("systemctl", "--system", "is-active", "--quiet", name).Run() == nil
	enabled = exec.Command("systemctl", "--system", "is-enabled", "--quiet", name).Run() == nil
	return running, enabled
}

// checkPackages queries dpkg-query for each declared package.
// Falls back to rpm -q if dpkg-query is not present.
func checkPackages(packages []ManifestPackage) []string {
	var deviations []string
	for _, p := range packages {
		if err := checkPackage(p); err != nil {
			deviations = append(deviations, fmt.Sprintf("pkg:%s:%s", err.Error(), p.Name))
		}
	}
	return deviations
}

func checkPackage(p ManifestPackage) error {
	assert.True(p.Name != "", "package name must not be empty")

	installed, err := queryPackageVersion(p.Name)
	if err != nil {
		return fmt.Errorf("missing")
	}
	if p.Version != "" && installed != p.Version {
		return fmt.Errorf("version_mismatch:%s:%s", installed, p.Version)
	}
	return nil
}

func queryPackageVersion(name string) (string, error) {
	// Try dpkg-query first (Debian/Ubuntu).
	out, err := exec.Command("dpkg-query", "-W", "-f=${Version}", name).Output()
	if err == nil {
		v := strings.TrimSpace(string(out))
		if v != "" {
			return v, nil
		}
	}

	// Fall back to rpm (RedHat/RHEL/CentOS).
	out, err = exec.Command("rpm", "-q", "--qf", "%{VERSION}-%{RELEASE}", name).Output()
	if err == nil {
		v := strings.TrimSpace(string(out))
		if v != "" && !strings.Contains(v, "not installed") {
			return v, nil
		}
	}

	return "", fmt.Errorf("package %q not found", name)
}

// checkService queries systemctl for each declared service.
func checkServices(services []ManifestService) []string {
	var deviations []string
	for _, s := range services {
		deviations = append(deviations, checkService(s)...)
	}
	return deviations
}

// checkService reports deviations for one declared service. When systemd
// cannot confirm a state because the caller lacks D-Bus permission (a common
// case for non-root service accounts: systemctl silently answers "inactive" /
// "no such unit" instead of the real state), it reports an explicit
// svc:unverifiable alert with remediation guidance instead of a false
// inactive/disabled verdict.
func checkService(s ManifestService) []string {
	assert.True(s.Name != "", "service name must not be empty")

	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil // non-systemd init; skip silently
	}

	var deviations []string

	if s.Running {
		deviations = append(deviations, checkServiceState(s.Name, "is-active", "svc:inactive")...)
	}

	if s.Enabled {
		deviations = append(deviations, checkServiceState(s.Name, "is-enabled", "svc:disabled")...)
	}

	return deviations
}

// checkServiceState runs one systemctl state verb (is-active / is-enabled).
func checkServiceState(name, verb, realDeviation string) []string {
	out, err := exec.Command("systemctl", "--system", verb, name).Output()
	if err == nil {
		return nil
	}
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}
	return mapSystemctlState(name, verb, realDeviation, string(out), exitCode)
}

// mapSystemctlState maps a systemctl result to a deviation list.
// rc 0 = state confirmed as expected (running/enabled). rc 3 = genuinely
// inactive. rc 1 = genuinely disabled. rc 4 = "no such unit" — for a declared
// service the unit must exist, so this signals a denied manager query
// (permission) rather than a real state change.
func mapSystemctlState(name, verb, realDeviation, out string, exitCode int) []string {
	switch exitCode {
	case 0:
		return nil
	case 4:
		state := strings.TrimSpace(out)
		return []string{fmt.Sprintf(
			"svc:unverifiable:%s:systemctl reports %q for %s (D-Bus permission denied — grant the service user read access to the systemd manager, e.g. /etc/dbus-1/system.d/fiia-fleet.conf, or run as root)",
			name, state, verb)}
	default:
		return []string{fmt.Sprintf("%s:%s", realDeviation, name)}
	}
}

// checkUnauthorizedPackages reports packages currently installed that were not
// present at provisioning time. snapshot is the PackageSnapshot from the manifest
// (populated only when mode: snapshot was used); nil snapshot disables this check.
func checkUnauthorizedPackages(snapshot []string) []string {
	if len(snapshot) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(snapshot))
	for _, p := range snapshot {
		allowed[p] = true
	}
	current, err := listInstalledPackages()
	if err != nil {
		return nil // best-effort; don't raise false drift on package manager absence
	}
	var deviations []string
	for _, p := range current {
		if !allowed[p] {
			deviations = append(deviations, fmt.Sprintf("pkg:unauthorized:%s", p))
		}
	}
	return deviations
}

// checkUnauthorizedServices reports services currently active that were not
// present at provisioning time.
func checkUnauthorizedServices(snapshot []string) []string {
	if len(snapshot) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(snapshot))
	for _, s := range snapshot {
		allowed[s] = true
	}
	current, err := listActiveServices()
	if err != nil {
		return nil
	}
	var deviations []string
	for _, s := range current {
		if !allowed[s] {
			deviations = append(deviations, fmt.Sprintf("svc:unauthorized:%s", s))
		}
	}
	return deviations
}

func listInstalledPackages() ([]string, error) {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Package}\n").Output()
	if err == nil {
		return splitLines(string(out)), nil
	}
	out, err = exec.Command("rpm", "-qa", "--qf", "%{NAME}\n").Output()
	if err == nil {
		return splitLines(string(out)), nil
	}
	return nil, fmt.Errorf("no package manager found")
}

func listActiveServices() ([]string, error) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil, nil // non-systemd init; no services to report
	}
	out, err := exec.Command("systemctl", "--system", "list-units", "--type=service",
		"--state=active", "--no-pager", "--no-legend", "--plain").Output()
	if err != nil {
		return nil, err
	}
	var services []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			name := strings.TrimSuffix(fields[0], ".service")
			if name != "" {
				services = append(services, name)
			}
		}
	}
	return services, nil
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}
