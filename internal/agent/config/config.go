package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/gleicon/fiia/internal/assert"
)

const (
	heartbeat_interval_sec_default = 60
	audit_interval_sec_default     = 1200
	audit_jitter_max_sec_default   = 120
)

// AgentConfig is the hub-less agent configuration. Drift verdicts and
// liveness go to the operator's OTel pipeline (OTLP endpoint or stdout);
// there is no hub address, TLS identity, or shared secret.
type AgentConfig struct {
	NodeID               string
	ManifestPath         string
	HeartbeatIntervalSec int
	AuditIntervalSec     int
	AuditJitterMaxSec    int
	OTLPEndpoint         string
	Remediate            bool
	CheckSet             []string
	FastScan             bool
}

type agentTOML struct {
	Agent agentSection `toml:"agent"`
}

type agentSection struct {
	NodeID               string   `toml:"node_id"`
	ManifestPath         string   `toml:"manifest_path"`
	HeartbeatIntervalSec int      `toml:"heartbeat_interval_sec"`
	AuditIntervalSec     int      `toml:"audit_interval_sec"`
	AuditJitterMaxSec    int      `toml:"audit_jitter_max_sec"`
	OTLPEndpoint         string   `toml:"otlp_endpoint"`
	Remediate            bool     `toml:"remediate"`
	CheckSet             []string `toml:"check"`
	FastScan             bool     `toml:"fast_scan"`
}

// Load reads the agent TOML configuration file at path.
// Only manifest_path is required; node_id defaults to the hostname at runtime.
func Load(path string) (*AgentConfig, error) {
	assert.True(path != "", "path must not be empty")

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	assert.True(len(data) > 0, "config file must not be empty")

	var raw agentTOML
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if raw.Agent.ManifestPath == "" {
		return nil, fmt.Errorf("invalid config: manifest_path is required")
	}

	cfg := &AgentConfig{
		NodeID:               raw.Agent.NodeID,
		ManifestPath:         raw.Agent.ManifestPath,
		HeartbeatIntervalSec: heartbeat_interval_sec_default,
		AuditIntervalSec:     audit_interval_sec_default,
		AuditJitterMaxSec:    audit_jitter_max_sec_default,
		OTLPEndpoint:         raw.Agent.OTLPEndpoint,
		Remediate:            raw.Agent.Remediate,
		CheckSet:             raw.Agent.CheckSet,
		FastScan:             raw.Agent.FastScan,
	}

	if raw.Agent.HeartbeatIntervalSec > 0 {
		cfg.HeartbeatIntervalSec = raw.Agent.HeartbeatIntervalSec
	}
	if raw.Agent.AuditIntervalSec > 0 {
		cfg.AuditIntervalSec = raw.Agent.AuditIntervalSec
	}
	if raw.Agent.AuditJitterMaxSec > 0 {
		cfg.AuditJitterMaxSec = raw.Agent.AuditJitterMaxSec
	}

	return cfg, nil
}
