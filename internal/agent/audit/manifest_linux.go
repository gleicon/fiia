//go:build linux

package audit

import (
	"fmt"
	"os/exec"
	"strings"
)

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
	assert(p.Name != "", "package name must not be empty")

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

// checkServices queries systemctl for each declared service.
func checkServices(services []ManifestService) []string {
	var deviations []string
	for _, s := range services {
		deviations = append(deviations, checkService(s)...)
	}
	return deviations
}

func checkService(s ManifestService) []string {
	assert(s.Name != "", "service name must not be empty")

	if _, err := exec.LookPath("systemctl"); err != nil {
		return nil // non-systemd init; skip silently
	}

	var deviations []string

	if s.Running {
		err := exec.Command("systemctl", "is-active", "--quiet", s.Name).Run()
		if err != nil {
			deviations = append(deviations, fmt.Sprintf("svc:inactive:%s", s.Name))
		}
	}

	if s.Enabled {
		err := exec.Command("systemctl", "is-enabled", "--quiet", s.Name).Run()
		if err != nil {
			deviations = append(deviations, fmt.Sprintf("svc:disabled:%s", s.Name))
		}
	}

	return deviations
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
	out, err := exec.Command("systemctl", "list-units", "--type=service",
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
