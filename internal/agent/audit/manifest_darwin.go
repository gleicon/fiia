//go:build darwin

package audit

import (
	"fmt"
	"os/exec"
	"strings"
)

// On macOS Homebrew is the package manager: versions and inventories come
// from brew, service states from brew services.

func brewOK() bool {
	_, err := exec.LookPath("brew")
	return err == nil
}

// checkPackages verifies declared packages via brew list --versions.
func checkPackages(packages []ManifestPackage) []string {
	var deviations []string
	for _, p := range packages {
		if err := checkBrewPackage(p); err != nil {
			deviations = append(deviations, "pkg:"+err.Error()+":"+p.Name)
		}
	}
	return deviations
}

func checkBrewPackage(p ManifestPackage) error {
	installed, ok := LookupPackageVersion(p.Name)
	if !ok {
		return fmt.Errorf("missing")
	}
	if p.Version != "" && installed != p.Version {
		return fmt.Errorf("version_mismatch:%s:%s", installed, p.Version)
	}
	return nil
}

// LookupPackageVersion returns the installed brew formula version,
// or ("", false) when absent.
func LookupPackageVersion(name string) (string, bool) {
	if !brewOK() {
		return "", false
	}
	out, err := exec.Command("brew", "list", "--versions", name).Output()
	if err != nil {
		return "", false
	}
	// Format: "<name> <version> [<version> ...]".
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 || fields[0] != name {
		return "", false
	}
	return fields[1], true
}

// ListInstalledPackages returns installed formulae and casks via brew list.
func ListInstalledPackages() []string {
	pkgs, err := listInstalledPackages()
	if err != nil {
		return nil
	}
	return pkgs
}

func listInstalledPackages() ([]string, error) {
	if !brewOK() {
		return nil, fmt.Errorf("brew not found")
	}
	out, err := exec.Command("brew", "list", "-1").Output()
	if err != nil {
		return nil, err
	}
	return splitLines(string(out)), nil
}

// brewService describes one row of `brew services list`.
type brewService struct {
	name    string
	running bool
	enabled bool
}

func listBrewServices() []brewService {
	if !brewOK() {
		return nil
	}
	out, err := exec.Command("brew", "services", "list").Output()
	if err != nil {
		return nil
	}
	var svcs []brewService
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if fields[0] == "Name" {
			continue // header
		}
		svcs = append(svcs, brewService{
			name:    fields[0],
			running: fields[1] == "started",
			// File column: a plist path means launchd-managed (enabled);
			// "none" means no plist (disabled).
			enabled: len(fields) >= 4 && fields[len(fields)-1] != "none",
		})
	}
	return svcs
}

// QueryServiceStates reports running/enabled via brew services.
func QueryServiceStates(name string) (running, enabled bool) {
	for _, s := range listBrewServices() {
		if s.name == name {
			return s.running, s.enabled
		}
	}
	return false, false
}

// checkServices verifies declared services via brew services.
func checkServices(services []ManifestService) []string {
	var deviations []string
	for _, s := range services {
		running, enabled := QueryServiceStates(s.Name)
		if s.Running && !running {
			deviations = append(deviations, "svc:inactive:"+s.Name)
		}
		if s.Enabled && !enabled {
			deviations = append(deviations, "svc:disabled:"+s.Name)
		}
	}
	return deviations
}

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
		return nil
	}
	var deviations []string
	for _, p := range current {
		if !allowed[p] {
			deviations = append(deviations, "pkg:unauthorized:"+p)
		}
	}
	return deviations
}

func checkUnauthorizedServices(snapshot []string) []string {
	if len(snapshot) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(snapshot))
	for _, s := range snapshot {
		allowed[s] = true
	}
	var deviations []string
	for _, s := range listBrewServices() {
		if s.running && !allowed[s.name] {
			deviations = append(deviations, "svc:unauthorized:"+s.name)
		}
	}
	return deviations
}

func ListActiveServices() []string {
	var out []string
	for _, s := range listBrewServices() {
		if s.running {
			out = append(out, s.name)
		}
	}
	return out
}

func listActiveServices() ([]string, error) {
	if !brewOK() {
		return nil, fmt.Errorf("brew not found")
	}
	return ListActiveServices(), nil
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
