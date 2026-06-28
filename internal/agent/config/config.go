package config

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

const (
	heartbeat_interval_sec_default = 300
	audit_interval_sec_default     = 1200
	audit_jitter_max_sec_default   = 120
	audit_timeout_sec_default      = 600
	connect_timeout_sec_default    = 10
)

type AgentConfig struct {
	NodeID               string
	HubAddr              string
	CACertPath           string
	HMACSecret           []byte
	HeartbeatIntervalSec int
	ConnectTimeoutSec    int
	AnsiblePlaybookPath  string
	DriftLogPath         string
	AuditIntervalSec     int
	AuditJitterMaxSec    int
	AuditTimeoutSec      int
	QueueDir             string
}

type agentTOML struct {
	Agent agentSection `toml:"agent"`
}

type agentSection struct {
	NodeID               string `toml:"node_id"`
	HubAddr              string `toml:"hub_addr"`
	CACertPath           string `toml:"ca_cert_path"`
	HMACSecretHex        string `toml:"hmac_secret_hex"`
	HeartbeatIntervalSec int    `toml:"heartbeat_interval_sec"`
	ConnectTimeoutSec    int    `toml:"connect_timeout_sec"`
	AnsiblePlaybookPath  string `toml:"ansible_playbook_path"`
	DriftLogPath         string `toml:"drift_log_path"`
	AuditIntervalSec     int    `toml:"audit_interval_sec"`
	AuditJitterMaxSec    int    `toml:"audit_jitter_max_sec"`
	AuditTimeoutSec      int    `toml:"audit_timeout_sec"`
	QueueDir             string `toml:"queue_dir"`
}

func assert(condition bool, message string) {
	if !condition {
		panic("agent/config: assertion failed: " + message)
	}
}

// Load reads and validates the agent TOML configuration file at path.
func Load(path string) (*AgentConfig, error) {
	assert(path != "", "path must not be empty")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	assert(len(data) > 0, "config file must not be empty")

	var raw agentTOML
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if err := validateSection(raw.Agent); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	secret_bytes, err := hex.DecodeString(raw.Agent.HMACSecretHex)
	if err != nil {
		return nil, fmt.Errorf("decode hmac_secret_hex: %w", err)
	}
	if len(secret_bytes) < 16 {
		return nil, fmt.Errorf("hmac_secret_hex must decode to at least 16 bytes, got %d", len(secret_bytes))
	}

	cfg := &AgentConfig{
		NodeID:               raw.Agent.NodeID,
		HubAddr:              raw.Agent.HubAddr,
		CACertPath:           raw.Agent.CACertPath,
		HMACSecret:           secret_bytes,
		HeartbeatIntervalSec: heartbeat_interval_sec_default,
		ConnectTimeoutSec:    connect_timeout_sec_default,
		AnsiblePlaybookPath:  raw.Agent.AnsiblePlaybookPath,
		DriftLogPath:         raw.Agent.DriftLogPath,
		AuditIntervalSec:     audit_interval_sec_default,
		AuditJitterMaxSec:    audit_jitter_max_sec_default,
		AuditTimeoutSec:      audit_timeout_sec_default,
	}

	if raw.Agent.HeartbeatIntervalSec > 0 {
		cfg.HeartbeatIntervalSec = raw.Agent.HeartbeatIntervalSec
	}
	if raw.Agent.ConnectTimeoutSec > 0 {
		cfg.ConnectTimeoutSec = raw.Agent.ConnectTimeoutSec
	}
	if raw.Agent.AuditIntervalSec > 0 {
		cfg.AuditIntervalSec = raw.Agent.AuditIntervalSec
	}
	if raw.Agent.AuditJitterMaxSec > 0 {
		cfg.AuditJitterMaxSec = raw.Agent.AuditJitterMaxSec
	}
	if raw.Agent.AuditTimeoutSec > 0 {
		cfg.AuditTimeoutSec = raw.Agent.AuditTimeoutSec
	}
	if raw.Agent.DriftLogPath == "" {
		cfg.DriftLogPath = "/var/log/fiia/drift.log"
	}
	cfg.QueueDir = raw.Agent.QueueDir
	if cfg.QueueDir == "" {
		cfg.QueueDir = "/var/lib/fiia/queue"
	}

	assert(cfg.NodeID != "", "parsed node_id must not be empty")
	assert(len(cfg.HMACSecret) >= 16, "parsed hmac_secret must be at least 16 bytes")
	return cfg, nil
}

func validateSection(s agentSection) error {
	if s.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if s.HubAddr == "" {
		return fmt.Errorf("hub_addr is required")
	}
	if s.CACertPath == "" {
		return fmt.Errorf("ca_cert_path is required")
	}
	if s.HMACSecretHex == "" {
		return fmt.Errorf("hmac_secret_hex is required")
	}
	return nil
}
