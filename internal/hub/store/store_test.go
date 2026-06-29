package store_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/gleicon/fiia/internal/hub/store"
)

func TestOpenUnknownDriver(t *testing.T) {
	_, err := store.Open("mysql", "some-dsn")
	if err == nil {
		t.Fatal("expected error for unknown driver, got nil")
	}
	if !strings.Contains(err.Error(), "unknown db_driver") {
		t.Errorf("error should mention 'unknown db_driver', got: %v", err)
	}
}

func TestSetAlertFirstSeenSemantics(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "alert_test.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	const node_id = "n1"
	const alert_type = "AGENT_PAUSED"
	const first_ts int64 = 1700000000
	const second_ts int64 = 1700001000

	if err := s.SetAlert(node_id, alert_type, first_ts); err != nil {
		t.Fatalf("first SetAlert: %v", err)
	}
	// Second call must be a no-op — created_unix must not change.
	if err := s.SetAlert(node_id, alert_type, second_ts); err != nil {
		t.Fatalf("second SetAlert: %v", err)
	}

	alerts, err := s.GetAlerts()
	if err != nil {
		t.Fatalf("GetAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("alert count: got %d, want 1", len(alerts))
	}
	if alerts[0].CreatedUnix != first_ts {
		t.Errorf("created_unix: got %d, want %d (first-seen semantics violated)", alerts[0].CreatedUnix, first_ts)
	}
}
