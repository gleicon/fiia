//go:build linux

package audit

import (
	"strings"
	"testing"
)

func TestMapSystemctlState(t *testing.T) {
	cases := []struct {
		name         string
		exitCode     int
		wantNil      bool
		wantContains string
	}{
		{name: "active exit 0 confirms running", exitCode: 0, wantNil: true},
		{name: "inactive exit 3 is a real deviation", exitCode: 3, wantContains: "svc:inactive:fiia-test"},
		{name: "disabled exit 1 is a real deviation", exitCode: 1, wantContains: "svc:disabled:fiia-test"},
		{name: "no-such-unit exit 4 is unverifiable (permission)", exitCode: 4, wantContains: "svc:unverifiable:fiia-test"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verb := "is-active"
			realDeviation := "svc:inactive"
			if tc.exitCode == 1 {
				verb = "is-enabled"
				realDeviation = "svc:disabled"
			}
			got := mapSystemctlState("fiia-test", verb, realDeviation, "inactive", tc.exitCode)
			if tc.wantNil {
				if len(got) != 0 {
					t.Fatalf("expected no deviations, got %v", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], tc.wantContains) {
				t.Fatalf("expected deviation containing %q, got %v", tc.wantContains, got)
			}
			if tc.exitCode == 4 && !strings.Contains(got[0], "D-Bus permission denied") {
				t.Fatalf("unverifiable deviation should carry remediation guidance, got %v", got[0])
			}
		})
	}
}
