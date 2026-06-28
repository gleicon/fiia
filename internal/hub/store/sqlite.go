package store

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations_fs embed.FS

func assert(condition bool, message string) {
	if !condition {
		panic("hub/store: assertion failed: " + message)
	}
}

// SQLiteStore implements Store using a local SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the SQLite database at path and runs migrations.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	assert(path != "", "path must not be empty")

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

func runMigrations(db *sql.DB) error {
	assert(db != nil, "db must not be nil")

	goose.SetBaseFS(migrations_fs)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	return goose.Up(db, "migrations")
}

// UpdateHeartbeat records the latest heartbeat timestamp for a node.
// Uses upsert: creates the node row if it doesn't exist.
func (s *SQLiteStore) UpdateHeartbeat(node_id string, timestamp_unix int64) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(timestamp_unix > 0, "timestamp_unix must be positive")

	_, err := s.db.Exec(
		`INSERT INTO nodes (node_id, last_seen_unix, status)
		 VALUES (?, ?, 'OK')
		 ON CONFLICT(node_id) DO UPDATE SET
		   last_seen_unix = excluded.last_seen_unix,
		   status = 'OK'`,
		node_id, timestamp_unix,
	)
	if err != nil {
		return fmt.Errorf("update heartbeat for %q: %w", node_id, err)
	}
	return nil
}

// GetNodeSecret returns the HMAC secret for a node. Used by ingest for validation.
func (s *SQLiteStore) GetNodeSecret(node_id string) ([]byte, error) {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")

	var secret_bytes []byte
	err := s.db.QueryRow(
		`SELECT secret_bytes FROM node_secrets WHERE node_id = ?`,
		node_id,
	).Scan(&secret_bytes)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("unknown node %q", node_id)
	}
	if err != nil {
		return nil, fmt.Errorf("get node secret for %q: %w", node_id, err)
	}
	assert(len(secret_bytes) > 0, "retrieved secret_bytes must not be empty")
	return secret_bytes, nil
}

// SetNodeSecret stores or updates the HMAC secret for a node.
func (s *SQLiteStore) SetNodeSecret(node_id string, secret_bytes []byte) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(len(secret_bytes) > 0, "secret_bytes must not be empty")

	created_unix := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT INTO node_secrets (node_id, secret_bytes, created_unix)
		 VALUES (?, ?, ?)
		 ON CONFLICT(node_id) DO UPDATE SET
		   secret_bytes = excluded.secret_bytes,
		   created_unix = excluded.created_unix`,
		node_id, secret_bytes, created_unix,
	)
	if err != nil {
		return fmt.Errorf("set node secret for %q: %w", node_id, err)
	}
	return nil
}

// SetAlert creates or updates an alert on a node.
func (s *SQLiteStore) SetAlert(node_id, alert_type string, created_unix int64) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(alert_type != "", "alert_type must not be empty")
	assert(created_unix > 0, "created_unix must be positive")

	_, err := s.db.Exec(
		`INSERT INTO alerts (node_id, alert_type, created_unix)
		 VALUES (?, ?, ?)
		 ON CONFLICT(node_id, alert_type) DO NOTHING`,
		node_id, alert_type, created_unix,
	)
	if err != nil {
		return fmt.Errorf("set alert %q for %q: %w", alert_type, node_id, err)
	}
	return nil
}

// ClearAlert removes an alert from a node.
func (s *SQLiteStore) ClearAlert(node_id, alert_type string) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(alert_type != "", "alert_type must not be empty")

	_, err := s.db.Exec(
		`DELETE FROM alerts WHERE node_id = ? AND alert_type = ?`,
		node_id, alert_type,
	)
	if err != nil {
		return fmt.Errorf("clear alert %q for %q: %w", alert_type, node_id, err)
	}
	return nil
}

// GetAlerts returns all active alerts ordered by node_id.
func (s *SQLiteStore) GetAlerts() ([]Alert, error) {
	assert(s.db != nil, "db must not be nil")

	rows, err := s.db.Query(
		`SELECT node_id, alert_type, created_unix FROM alerts ORDER BY node_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("get alerts: %w", err)
	}
	defer rows.Close()

	var alerts []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.NodeID, &a.AlertType, &a.CreatedUnix); err != nil {
			return nil, fmt.Errorf("scan alert row: %w", err)
		}
		alerts = append(alerts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alert rows: %w", err)
	}
	return alerts, nil
}

// GetNodes returns all node records ordered by node_id.
func (s *SQLiteStore) GetNodes() ([]Node, error) {
	assert(s.db != nil, "db must not be nil")

	rows, err := s.db.Query(
		`SELECT node_id, last_seen_unix, status FROM nodes ORDER BY node_id`,
	)
	if err != nil {
		return nil, fmt.Errorf("get nodes: %w", err)
	}
	defer rows.Close()

	var nodes []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.ID, &n.LastSeenUnix, &n.Status); err != nil {
			return nil, fmt.Errorf("scan node row: %w", err)
		}
		nodes = append(nodes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node rows: %w", err)
	}
	return nodes, nil
}

// GetNode returns a single node record by ID.
func (s *SQLiteStore) GetNode(node_id string) (Node, error) {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")

	var n Node
	err := s.db.QueryRow(
		`SELECT node_id, last_seen_unix, status FROM nodes WHERE node_id = ?`,
		node_id,
	).Scan(&n.ID, &n.LastSeenUnix, &n.Status)
	if err == sql.ErrNoRows {
		return Node{}, fmt.Errorf("node %q not found", node_id)
	}
	if err != nil {
		return Node{}, fmt.Errorf("get node %q: %w", node_id, err)
	}
	assert(n.ID != "", "retrieved node_id must not be empty")
	return n, nil
}

// AppendDrift inserts a drift event record for a node.
func (s *SQLiteStore) AppendDrift(node_id string, timestamp_unix int64, status string, tasks_changed []string) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(status != "", "status must not be empty")

	joined := strings.Join(tasks_changed, "\n")
	_, err := s.db.Exec(
		`INSERT INTO drift_events (node_id, timestamp_unix, status, tasks_changed) VALUES (?, ?, ?, ?)`,
		node_id, timestamp_unix, status, joined,
	)
	if err != nil {
		return fmt.Errorf("append drift for %q: %w", node_id, err)
	}
	return nil
}

// GetDriftEvents returns the most recent drift events for a node, newest first.
func (s *SQLiteStore) GetDriftEvents(node_id string, limit int) ([]DriftEvent, error) {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(limit > 0, "limit must be positive")

	rows, err := s.db.Query(
		`SELECT node_id, timestamp_unix, status, tasks_changed
		 FROM drift_events WHERE node_id = ?
		 ORDER BY timestamp_unix DESC LIMIT ?`,
		node_id, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get drift events for %q: %w", node_id, err)
	}
	defer rows.Close()

	var events []DriftEvent
	for rows.Next() {
		var e DriftEvent
		var joined string
		if err := rows.Scan(&e.NodeID, &e.TimestampUnix, &e.Status, &joined); err != nil {
			return nil, fmt.Errorf("scan drift event: %w", err)
		}
		if joined != "" {
			e.TasksChanged = strings.Split(joined, "\n")
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate drift events: %w", err)
	}
	return events, nil
}

// CountNodesWithStatus returns the count of nodes whose status matches the given value.
func (s *SQLiteStore) CountNodesWithStatus(status string) (int64, error) {
	assert(s.db != nil, "db must not be nil")
	assert(status != "", "status must not be empty")

	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE status = ?`, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count nodes with status %q: %w", status, err)
	}
	return count, nil
}

// Close releases the database connection.
func (s *SQLiteStore) Close() error {
	assert(s.db != nil, "db must not be nil")
	return s.db.Close()
}
