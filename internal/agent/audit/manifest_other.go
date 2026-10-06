//go:build !linux && !darwin

package audit

// checkPackages and checkServices are no-ops on non-Linux platforms.
// The agent is designed for Linux fleet nodes; this stub allows the package
// to compile on macOS for development and testing.

func checkPackages(_ []ManifestPackage) []string    { return nil }
func checkServices(_ []ManifestService) []string    { return nil }
func checkUnauthorizedPackages(_ []string) []string { return nil }
func checkUnauthorizedServices(_ []string) []string { return nil }

// Non-Linux stubs for manifest generation helpers (see manifest_linux.go).
// Generation is a Linux-target operation; on other platforms package/service
// lookups report absent so file-only manifests still work in dev.

func LookupPackageVersion(_ string) (string, bool) { return "", false }
func ListInstalledPackages() []string              { return nil }
func ListActiveServices() []string                 { return nil }

func QueryServiceStates(_ string) (running, enabled bool) { return false, false }
