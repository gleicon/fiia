-- +goose Up

CREATE TABLE IF NOT EXISTS nodes (
    node_id        TEXT   PRIMARY KEY,
    last_seen_unix BIGINT NOT NULL DEFAULT 0,
    status         TEXT   NOT NULL DEFAULT 'UNKNOWN'
);

CREATE TABLE IF NOT EXISTS node_secrets (
    node_id      TEXT   PRIMARY KEY,
    secret_bytes BYTEA  NOT NULL CHECK (octet_length(secret_bytes) >= 16),
    created_unix BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS alerts (
    node_id      TEXT   NOT NULL,
    alert_type   TEXT   NOT NULL,
    created_unix BIGINT NOT NULL,
    PRIMARY KEY (node_id, alert_type)
);

-- +goose Down

DROP TABLE IF EXISTS alerts;
DROP TABLE IF EXISTS node_secrets;
DROP TABLE IF EXISTS nodes;
