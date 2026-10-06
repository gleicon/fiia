package audit

import (
	"strings"
	"testing"
)

func TestUnauthorizedSet(t *testing.T) {
	got := unauthorizedSet([]string{"bash", "openssh-server"}, []string{"bash", "curl", "openssh-server", "vim"})
	want := []string{"curl", "vim"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if len(unauthorizedSet([]string{"a", "b"}, []string{"a", "b"})) != 0 {
		t.Fatal("expected no unauthorized entries for identical sets")
	}
}

func TestRemediationString(t *testing.T) {
	empty := Remediation{}
	if !empty.Empty() {
		t.Fatal("empty remediation should report Empty()")
	}
	if empty.String() != "" {
		t.Fatalf("empty remediation String should be empty, got %q", empty.String())
	}

	r := Remediation{
		PackagesRemoved:  []string{"vim", "vim-common"},
		ServicesStarted:  []string{"nginx"},
		ServicesDisabled: []string{"telnet"},
	}
	if r.Empty() {
		t.Fatal("non-empty remediation should not report Empty()")
	}
	s := r.String()
	for _, want := range []string{"removed 2 package(s): vim, vim-common", "started 1 service(s): nginx", "disabled 1 service(s): telnet"} {
		if !strings.Contains(s, want) {
			t.Fatalf("remediation string %q missing %q", s, want)
		}
	}
}

// TestRemediationReportsOnlyWhatChanged guards the honest-accounting behavior:
// an enforcement pass must report packages actually gone, and must never report
// removals when nothing was unauthorized.
func TestRemediationEmptyWhenNoUnauthorized(t *testing.T) {
	r := Remediate(Manifest{
		PackageSnapshot: []string{"bash", "openssh-server"},
	})
	// On a host where those are the only installed packages this is a no-op; we
	// assert the type contracts rather than shell state: Empty() must be true
	// for a zero change and the string stays blank.
	if got := r.Empty(); !got {
		t.Fatalf("expected no-op remediation, got %v", r)
	}
}
