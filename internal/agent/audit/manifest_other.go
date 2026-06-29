//go:build !linux

package audit

// checkPackages and checkServices are no-ops on non-Linux platforms.
// The agent is designed for Linux fleet nodes; this stub allows the package
// to compile on macOS for development and testing.

func checkPackages(_ []ManifestPackage) []string    { return nil }
func checkServices(_ []ManifestService) []string    { return nil }
func checkUnauthorizedPackages(_ []string) []string { return nil }
func checkUnauthorizedServices(_ []string) []string { return nil }
