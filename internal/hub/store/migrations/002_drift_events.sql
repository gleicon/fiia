-- +goose Up

CREATE TABLE IF NOT EXISTS drift_events (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    node_id        TEXT    NOT NULL,
    timestamp_unix INTEGER NOT NULL,
    status         TEXT    NOT NULL,
    tasks_changed  TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_drift_events_node_id ON drift_events (node_id);

-- +goose Down

DROP INDEX IF EXISTS idx_drift_events_node_id;
DROP TABLE IF EXISTS drift_events;
