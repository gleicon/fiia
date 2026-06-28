package store

import (
	"database/sql"
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed postgres_migrations/*.sql
var postgres_migrations_fs embed.FS

// PostgresStore implements Store using a Postgres database.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore opens a connection using dsn and runs migrations.
func NewPostgresStore(dsn string) (*PostgresStore, error) {
	assert(dsn != "", "dsn must not be empty")

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	if err := runPostgresMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("run postgres migrations: %w", err)
	}

	return &PostgresStore{db: db}, nil
}

func runPostgresMigrations(db *sql.DB) error {
	assert(db != nil, "db must not be nil")

	goose.SetBaseFS(postgres_migrations_fs)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	return goose.Up(db, "postgres_migrations")
}

func (s *PostgresStore) UpdateHeartbeat(node_id string, timestamp_unix int64) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(timestamp_unix > 0, "timestamp_unix must be positive")

	_, err := s.db.Exec(
		`INSERT INTO nodes (node_id, last_seen_unix, status)
		 VALUES ($1, $2, 'OK')
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

func (s *PostgresStore) GetNodeSecret(node_id string) ([]byte, error) {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")

	var secret_bytes []byte
	err := s.db.QueryRow(
		`SELECT secret_bytes FROM node_secrets WHERE node_id = $1`,
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

func (s *PostgresStore) SetNodeSecret(node_id string, secret_bytes []byte) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(len(secret_bytes) > 0, "secret_bytes must not be empty")

	created_unix := time.Now().Unix()
	_, err := s.db.Exec(
		`INSERT INTO node_secrets (node_id, secret_bytes, created_unix)
		 VALUES ($1, $2, $3)
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

func (s *PostgresStore) SetAlert(node_id, alert_type string, created_unix int64) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(alert_type != "", "alert_type must not be empty")
	assert(created_unix > 0, "created_unix must be positive")

	_, err := s.db.Exec(
		`INSERT INTO alerts (node_id, alert_type, created_unix)
		 VALUES ($1, $2, $3)
		 ON CONFLICT(node_id, alert_type) DO NOTHING`,
		node_id, alert_type, created_unix,
	)
	if err != nil {
		return fmt.Errorf("set alert %q for %q: %w", alert_type, node_id, err)
	}
	return nil
}

func (s *PostgresStore) ClearAlert(node_id, alert_type string) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(alert_type != "", "alert_type must not be empty")

	_, err := s.db.Exec(
		`DELETE FROM alerts WHERE node_id = $1 AND alert_type = $2`,
		node_id, alert_type,
	)
	if err != nil {
		return fmt.Errorf("clear alert %q for %q: %w", alert_type, node_id, err)
	}
	return nil
}

func (s *PostgresStore) GetAlerts() ([]Alert, error) {
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

func (s *PostgresStore) GetNodes() ([]Node, error) {
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

func (s *PostgresStore) GetNode(node_id string) (Node, error) {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")

	var n Node
	err := s.db.QueryRow(
		`SELECT node_id, last_seen_unix, status FROM nodes WHERE node_id = $1`,
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

func (s *PostgresStore) AppendDrift(node_id string, timestamp_unix int64, status string, tasks_changed []string) error {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(status != "", "status must not be empty")

	joined := strings.Join(tasks_changed, "\n")
	_, err := s.db.Exec(
		`INSERT INTO drift_events (node_id, timestamp_unix, status, tasks_changed)
		 VALUES ($1, $2, $3, $4)`,
		node_id, timestamp_unix, status, joined,
	)
	if err != nil {
		return fmt.Errorf("append drift for %q: %w", node_id, err)
	}
	return nil
}

func (s *PostgresStore) GetDriftEvents(node_id string, limit int) ([]DriftEvent, error) {
	assert(s.db != nil, "db must not be nil")
	assert(node_id != "", "node_id must not be empty")
	assert(limit > 0, "limit must be positive")

	rows, err := s.db.Query(
		`SELECT node_id, timestamp_unix, status, tasks_changed
		 FROM drift_events WHERE node_id = $1
		 ORDER BY timestamp_unix DESC LIMIT $2`,
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

func (s *PostgresStore) CountNodesWithStatus(status string) (int64, error) {
	assert(s.db != nil, "db must not be nil")
	assert(status != "", "status must not be empty")

	var count int64
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM nodes WHERE status = $1`, status,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count nodes with status %q: %w", status, err)
	}
	return count, nil
}

func (s *PostgresStore) Close() error {
	assert(s.db != nil, "db must not be nil")
	return s.db.Close()
}
