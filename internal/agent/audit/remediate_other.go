//go:build !linux

package audit

// Remediate is a no-op on non-Linux targets (no dpkg/rpm/systemctl). The
// manifest remains detectable; enforcement is only meaningful on Linux nodes.
func Remediate(m Manifest) Remediation {
	return Remediation{}
}
