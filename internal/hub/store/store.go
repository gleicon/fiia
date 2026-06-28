package store

import "fmt"

// Open selects a Store implementation by driver.
// For "sqlite", conn is a file path. For "postgres", conn is a connection DSN.
func Open(driver, conn string) (Store, error) {
	if driver == "" {
		return nil, fmt.Errorf("driver must not be empty")
	}
	if conn == "" {
		return nil, fmt.Errorf("conn must not be empty")
	}

	switch driver {
	case "sqlite":
		return NewSQLiteStore(conn)
	case "postgres":
		return NewPostgresStore(conn)
	default:
		return nil, fmt.Errorf("unknown db_driver %q: must be sqlite or postgres", driver)
	}
}

// Node represents a fleet node known to the hub.
type Node struct {
	ID           string
	LastSeenUnix int64
	Status       string
}

// Alert represents an active alert on a node.
type Alert struct {
	NodeID      string
	AlertType   string
	CreatedUnix int64
}

// DriftEvent records a drift detection result from an agent.
type DriftEvent struct {
	NodeID        string
	TimestampUnix int64
	Status        string
	TasksChanged  []string
}

// Store is the hub's storage abstraction. All methods are safe for concurrent use.
// SQLite is the MVP implementation; Postgres is the HA upgrade path.
type Store interface {
	// UpdateHeartbeat records the latest heartbeat timestamp for a node.
	// Creates the node record if it does not exist.
	UpdateHeartbeat(node_id string, timestamp_unix int64) error

	// GetNodeSecret returns the per-node HMAC secret for signature validation.
	// Returns an error if the node_id is unknown.
	GetNodeSecret(node_id string) ([]byte, error)

	// SetNodeSecret stores the per-node HMAC secret. Called during bootstrap registration.
	SetNodeSecret(node_id string, secret_bytes []byte) error

	// AppendDrift stores one drift event record for a node.
	AppendDrift(node_id string, timestamp_unix int64, status string, tasks_changed []string) error

	// GetDriftEvents returns the most recent drift events for a node (latest first).
	GetDriftEvents(node_id string, limit int) ([]DriftEvent, error)

	// SetAlert creates or updates an alert flag on a node.
	SetAlert(node_id, alert_type string, created_unix int64) error

	// ClearAlert removes an alert flag from a node.
	ClearAlert(node_id, alert_type string) error

	// GetAlerts returns all currently active alerts.
	GetAlerts() ([]Alert, error)

	// GetNodes returns all nodes and their last-seen timestamps.
	GetNodes() ([]Node, error)

	// GetNode returns a single node record by ID.
	GetNode(node_id string) (Node, error)

	// CountNodesWithStatus returns the count of nodes whose status matches the given value.
	CountNodesWithStatus(status string) (int64, error)

	// Close releases the store's resources.
	Close() error
}
