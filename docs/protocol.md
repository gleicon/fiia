# Fiia — Wire Protocol

## Transport

All communication uses TCP/TLS 1.3 with one-way server certificate authentication (agent verifies hub cert against a pinned root CA; no mTLS). Each send opens a new TCP connection and closes it on completion.

## Frame format

Every agent→hub frame uses a length-prefixed envelope:

```
┌──────────────────────────────────────────────────────────┐
│  4 bytes          │  N bytes        │  32 bytes          │
│  body length      │  msgpack        │  HMAC-SHA256       │
│  (big-endian u32) │  payload        │  sig               │
└──────────────────────────────────────────────────────────┘
 body length = N + 32
```

The HMAC-SHA256 is computed over the raw msgpack bytes. The hub reads `body length`, allocates exactly that many bytes, then validates the HMAC **before** decoding the msgpack. Any HMAC mismatch raises a `HMAC_MISMATCH` alert and drops the connection — the payload is never decoded.

## Hub→agent ACK frame

After storing a heartbeat, the hub writes a minimal acknowledgement frame:

```
┌──────────────────────────────────────┐
│  4 bytes          │  1 byte          │
│  length = 1       │  PayloadTypeAck  │
│  (big-endian u32) │  (0x02)          │
└──────────────────────────────────────┘
```

No HMAC — TLS authenticates the hub. The agent reads this with a 2-second timeout. On ACK received, the agent advances the disk queue past the just-sent frame. On timeout or missing ACK, the frame stays in the queue for the next heartbeat cycle.

## Payload types

| Constant | Value | Direction | Description |
|----------|-------|-----------|-------------|
| `PayloadTypeHeartbeat` | `0` | agent→hub | Periodic liveness + USE metrics |
| `PayloadTypeDrift` | `1` | agent→hub | Ansible audit result |
| `PayloadTypeAck` | `2` | hub→agent | Heartbeat acknowledgement |
| `PayloadTypeCommand` | `3` | hub→agent | Reserved — remote control (Increment 6) |

## Schema versioning

Every payload carries `SchemaVersion uint8`. The current version is `1`. The hub reads `SchemaVersion` before routing; unknown versions are dropped with a logged warning. Map encoding (not array) means new fields in future versions are silently ignored by older decoders — forward compatibility at zero cost.

## HeartbeatPayload

```go
type HeartbeatPayload struct {
    SchemaVersion uint8      `msgpack:"schema_version"`
    PayloadType   uint8      `msgpack:"payload_type"`   // PayloadTypeHeartbeat
    NodeID        string     `msgpack:"node_id"`
    TimestampUnix int64      `msgpack:"timestamp_unix"`
    Status        string     `msgpack:"status"`         // always "OK"
    Metrics       USEMetrics `msgpack:"metrics"`
}

type USEMetrics struct {
    CPUUtilPct  float32 `msgpack:"cpu_util_pct"`
    CPUSatPct   float32 `msgpack:"cpu_sat_pct"`
    MemUtilPct  float32 `msgpack:"mem_util_pct"`
    MemSatPct   float32 `msgpack:"mem_sat_pct"`
    DiskUtilPct float32 `msgpack:"disk_util_pct"`
    DiskSatPct  float32 `msgpack:"disk_sat_pct"`
    NetUtilBps  uint64  `msgpack:"net_util_bps"`
    NetErrCount uint64  `msgpack:"net_err_count"`
}
```

Approximate encoded size: ~96 bytes per heartbeat.

## DriftPayload

```go
type DriftPayload struct {
    SchemaVersion uint8    `msgpack:"schema_version"`
    PayloadType   uint8    `msgpack:"payload_type"`   // PayloadTypeDrift
    NodeID        string   `msgpack:"node_id"`
    TimestampUnix int64    `msgpack:"timestamp_unix"`
    Status        string   `msgpack:"status"`
    TasksChanged  []string `msgpack:"tasks_changed"`
}
```

### Drift status values

| Status | Source |
|--------|--------|
| `OK` | ansible exit 0 and PLAY RECAP `changed=0` |
| `DRIFT_DETECTED` | ansible exit 2 or PLAY RECAP `changed>0` |
| `AUDIT_TIMEOUT` | ansible did not complete within `audit_timeout_sec` |
| `AUDIT_ERROR` | ansible-playbook failed to start (binary missing, bad permissions) |
| `AUDIT_RESOURCE_EXCEEDED` | ansible killed by OOM (exit 137 / SIGKILL) |
| `AUDIT_EXIT_N` | ansible exited with unexpected code N |

## Authentication

Each node has a per-node HMAC-SHA256 secret (32 bytes, hex-encoded in `agent.toml`). The secret is delivered by the Ansible bootstrap playbook and stored in the hub's `node_secrets` table.

On each frame: the hub peeks `node_id` from the msgpack, looks up the secret, and verifies `HMAC-SHA256(secret, payload_bytes) == sig`. This happens before any further decode. A mismatch raises `HMAC_MISMATCH` and drops the connection.

The hub never sends HMAC'd frames back — TLS authenticates the hub to the agent.

## Store-and-forward queue

The agent writes every frame to a disk-backed ring buffer (`/var/lib/fiia/queue/`) before sending. The queue holds up to 64 frames (~12 KB at max fill). On hub reconnect, the queue replays in FIFO order. Overflow evicts the oldest entry silently — the most recent N audits are what matters operationally.

Queue state is persisted as a msgpack file with atomic rename. A crash between writing a slot and updating the state leaves at most one orphaned slot file, which is harmless on next open.
