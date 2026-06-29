package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

const (
	api_addr_default            = ":9091"
	listen_addr_default         = ":9443"
	db_path_default             = "/var/lib/fiia/hub.db"
	reconcile_interval_sec      = 3600
	rate_limit_rps_default      = 1.0
	rate_limit_burst_default    = 3
)

// HubConfig is the validated hub configuration.
// One HTTP port (APIAddr) serves both the REST API and /metrics + /healthz.
// One TLS port (ListenAddr) receives agent frames.
type HubConfig struct {
	ListenAddr           string
	CertPath             string
	KeyPath              string
	DBPath               string  // SQLite file path; used when DBDriver == "sqlite"
	DBDriver             string  // "sqlite" (default) or "postgres"
	DBDSN                string  // Postgres DSN; ignored when DBDriver == "sqlite"
	APIAddr              string  // serves REST API + /metrics + /healthz
	InventoryCSVPath     string
	ReconcileIntervalSec int
	RateLimitRPS         float64 // max heartbeats per second per node (default 1.0)
	RateLimitBurst       int     // token bucket burst size (default 3)
	EnrollmentToken      string  // if set, POST /nodes/{id}/enroll requires Bearer auth
	WebhookURL           string  // if set, alert set/clear fires an HTTP POST to this URL
}

type hubTOML struct {
	Hub hubSection `toml:"hub"`
}

type hubSection struct {
	ListenAddr           string  `toml:"listen_addr"`
	CertPath             string  `toml:"cert_path"`
	KeyPath              string  `toml:"key_path"`
	DBPath               string  `toml:"db_path"`
	DBDriver             string  `toml:"db_driver"`
	DBDSN                string  `toml:"db_dsn"`
	APIAddr              string  `toml:"api_addr"`
	InventoryCSVPath     string  `toml:"inventory_csv_path"`
	ReconcileIntervalSec int     `toml:"reconcile_interval_sec"`
	RateLimitRPS         float64 `toml:"rate_limit_rps"`
	RateLimitBurst       int     `toml:"rate_limit_burst"`
	EnrollmentToken      string  `toml:"enrollment_token"`
	WebhookURL           string  `toml:"alert_webhook_url"`
}

func assert(condition bool, message string) {
	if !condition {
		panic("hub/config: assertion failed: " + message)
	}
}

// Load reads and validates the hub TOML configuration file at path.
func Load(path string) (*HubConfig, error) {
	assert(path != "", "path must not be empty")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	assert(len(data) > 0, "config file must not be empty")

	var raw hubTOML
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := validateSection(raw.Hub); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	cfg := &HubConfig{
		ListenAddr:           listen_addr_default,
		CertPath:             raw.Hub.CertPath,
		KeyPath:              raw.Hub.KeyPath,
		DBPath:               db_path_default,
		DBDriver:             "sqlite",
		APIAddr:              api_addr_default,
		InventoryCSVPath:     raw.Hub.InventoryCSVPath,
		ReconcileIntervalSec: reconcile_interval_sec,
		RateLimitRPS:         rate_limit_rps_default,
		RateLimitBurst:       rate_limit_burst_default,
	}

	if raw.Hub.ListenAddr != "" {
		cfg.ListenAddr = raw.Hub.ListenAddr
	}
	if raw.Hub.DBPath != "" {
		cfg.DBPath = raw.Hub.DBPath
	}
	if raw.Hub.DBDriver != "" {
		cfg.DBDriver = raw.Hub.DBDriver
	}
	if raw.Hub.DBDSN != "" {
		cfg.DBDSN = raw.Hub.DBDSN
	}
	if raw.Hub.APIAddr != "" {
		cfg.APIAddr = raw.Hub.APIAddr
	}
	if raw.Hub.ReconcileIntervalSec > 0 {
		cfg.ReconcileIntervalSec = raw.Hub.ReconcileIntervalSec
	}
	if raw.Hub.RateLimitRPS > 0 {
		cfg.RateLimitRPS = raw.Hub.RateLimitRPS
	}
	if raw.Hub.RateLimitBurst > 0 {
		cfg.RateLimitBurst = raw.Hub.RateLimitBurst
	}
	if raw.Hub.EnrollmentToken != "" {
		cfg.EnrollmentToken = raw.Hub.EnrollmentToken
	}
	if raw.Hub.WebhookURL != "" {
		cfg.WebhookURL = raw.Hub.WebhookURL
	}

	assert(cfg.CertPath != "", "parsed cert_path must not be empty")
	assert(cfg.KeyPath != "", "parsed key_path must not be empty")
	return cfg, nil
}

func validateSection(s hubSection) error {
	if s.CertPath == "" {
		return fmt.Errorf("cert_path is required")
	}
	if s.KeyPath == "" {
		return fmt.Errorf("key_path is required")
	}
	return nil
}
