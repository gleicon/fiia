package heartbeat

import (
	"testing"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/wire"
)

func nopCancel() {}

func TestDispatchCommandAuditNow(t *testing.T) {
	cfg := &agentcfg.AgentConfig{}
	ch := make(chan struct{}, 1)

	dispatchCommand(&wire.CommandPayload{Command: "audit_now"}, cfg, ch, nopCancel)

	select {
	case <-ch:
	default:
		t.Fatal("audit_now_ch should have received a signal")
	}
}

func TestDispatchCommandAuditNowNonBlocking(t *testing.T) {
	cfg := &agentcfg.AgentConfig{}
	ch := make(chan struct{}, 1)
	ch <- struct{}{} // pre-fill so second dispatch would block if not handled

	done := make(chan struct{})
	go func() {
		dispatchCommand(&wire.CommandPayload{Command: "audit_now"}, cfg, ch, nopCancel)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("dispatchCommand blocked on a full audit_now_ch")
	}
}

func TestDispatchCommandConfigUpdateBothFields(t *testing.T) {
	cfg := &agentcfg.AgentConfig{
		AnsiblePlaybookPath: "/old/site.yml",
		AuditIntervalSec:    1200,
	}
	ch := make(chan struct{}, 1)

	dispatchCommand(&wire.CommandPayload{
		Command:      "config_update",
		PlaybookPath: "/new/site.yml",
		IntervalSec:  600,
	}, cfg, ch, nopCancel)

	if cfg.AnsiblePlaybookPath != "/new/site.yml" {
		t.Errorf("PlaybookPath: got %q, want /new/site.yml", cfg.AnsiblePlaybookPath)
	}
	if cfg.AuditIntervalSec != 600 {
		t.Errorf("AuditIntervalSec: got %d, want 600", cfg.AuditIntervalSec)
	}
}

func TestDispatchCommandConfigUpdatePartialInterval(t *testing.T) {
	cfg := &agentcfg.AgentConfig{
		AnsiblePlaybookPath: "/existing/site.yml",
		AuditIntervalSec:    1200,
	}
	ch := make(chan struct{}, 1)

	// Empty PlaybookPath must not overwrite the existing value.
	dispatchCommand(&wire.CommandPayload{
		Command:     "config_update",
		IntervalSec: 300,
	}, cfg, ch, nopCancel)

	if cfg.AnsiblePlaybookPath != "/existing/site.yml" {
		t.Errorf("PlaybookPath should be unchanged, got %q", cfg.AnsiblePlaybookPath)
	}
	if cfg.AuditIntervalSec != 300 {
		t.Errorf("AuditIntervalSec: got %d, want 300", cfg.AuditIntervalSec)
	}
}

func TestDispatchCommandConfigUpdatePartialPlaybook(t *testing.T) {
	cfg := &agentcfg.AgentConfig{
		AnsiblePlaybookPath: "/old/site.yml",
		AuditIntervalSec:    1200,
	}
	ch := make(chan struct{}, 1)

	// Zero IntervalSec must not overwrite the existing value.
	dispatchCommand(&wire.CommandPayload{
		Command:      "config_update",
		PlaybookPath: "/new/site.yml",
	}, cfg, ch, nopCancel)

	if cfg.AnsiblePlaybookPath != "/new/site.yml" {
		t.Errorf("PlaybookPath: got %q, want /new/site.yml", cfg.AnsiblePlaybookPath)
	}
	if cfg.AuditIntervalSec != 1200 {
		t.Errorf("AuditIntervalSec should be unchanged, got %d", cfg.AuditIntervalSec)
	}
}

func TestDispatchCommandUnknownNoOp(t *testing.T) {
	cfg := &agentcfg.AgentConfig{}
	ch := make(chan struct{}, 1)

	// Must not panic.
	dispatchCommand(&wire.CommandPayload{Command: "reformat_disk"}, cfg, ch, nopCancel)

	select {
	case <-ch:
		t.Fatal("unknown command should not signal audit_now_ch")
	default:
	}
}

// graceful_restart is not unit-tested: it calls syscall.Kill(os.Getpid(), SIGTERM)
// which would terminate the test process. Cover in an integration/e2e test instead.
