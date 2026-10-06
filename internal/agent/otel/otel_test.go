package otel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gleicon/fiia/internal/agent/audit"
)

func TestSetupEmitShutdownStdout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	h, shutdown, err := Setup(ctx, "", "test-node")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	h.EmitAlive(ctx)
	h.EmitCheck(ctx, audit.CheckResult{
		NodeID:        "test-node",
		TimestampUnix: time.Now().Unix(),
		Status:        audit.StatusDriftDetected,
		Deviations:    []string{"file:hash_mismatch:/etc/motd"},
	})
	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestDriftStatusMapping(t *testing.T) {
	cases := map[string]int64{
		audit.StatusOK:               0,
		audit.StatusDriftDetected:    1,
		audit.StatusManifestNotFound: 2,
		"something-else":             2,
	}
	for status, want := range cases {
		if got := driftStatus(status); got != want {
			t.Errorf("driftStatus(%q): got %d, want %d", status, got, want)
		}
	}
}

func TestJoinDeviationsCap(t *testing.T) {
	short := []string{"file:hash_mismatch:/etc/motd"}
	if got := joinDeviations(short); got != "file:hash_mismatch:/etc/motd" {
		t.Fatalf("short join wrong: %q", got)
	}

	long := make([]string, 0)
	for i := 0; i < 100; i++ {
		long = append(long, strings.Repeat("x", 50))
	}
	got := joinDeviations(long)
	if len(got) > maxDeviationLabelLen+4 {
		t.Fatalf("join not capped: len=%d", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("capped join should end with ellipsis: %q", got)
	}
}
