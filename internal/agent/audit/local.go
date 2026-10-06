package audit

import (
	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/assert"
)

// LoadFile reads and validates the manifest at path.
// Public wrapper over loadManifest for local/lint mode.
func LoadFile(path string) (Manifest, error) {
	assert.True(path != "", "path must not be empty")
	return loadManifest(path)
}

// CheckLocal runs a one-shot manifest drift check without any hub interaction.
// It builds a minimal agent config and reuses RunManifest, so local results
// match what the daemon would emit. Returns (result, false) only when
// manifestPath is empty.
func CheckLocal(manifestPath, nodeID string) (CheckResult, bool) {
	assert.True(manifestPath != "", "manifestPath must not be empty")
	if nodeID == "" {
		nodeID = "localhost"
	}
	cfg := &agentcfg.AgentConfig{
		NodeID:       nodeID,
		ManifestPath: manifestPath,
	}
	return RunManifest(cfg)
}
