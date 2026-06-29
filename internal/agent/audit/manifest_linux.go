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
