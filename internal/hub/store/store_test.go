package store_test

import (
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
