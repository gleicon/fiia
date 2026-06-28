package wire

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

const (
	SchemaVersionCurrent uint8 = 1
	PayloadTypeHeartbeat uint8 = 0
	PayloadTypeDrift     uint8 = 1
	// PayloadTypeAck and PayloadTypeCommand are reserved for the hub→agent
	// command channel (Increment 6). Not yet implemented — hub sends no frames today.
	PayloadTypeAck     uint8 = 2
	PayloadTypeCommand uint8 = 3
	HMACSize                 = 32
	// FrameHeaderSize is the 4-byte big-endian length prefix.
	FrameHeaderSize = 4
	// FrameMinSize is the minimum valid frame: header + at least 1 payload byte + HMAC.
	FrameMinSize = FrameHeaderSize + 1 + HMACSize
)

// USEMetrics holds Brendan Gregg's USE method snapshot for a host.
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

// HeartbeatPayload is the periodic liveness + telemetry message sent by each agent.
type HeartbeatPayload struct {
	SchemaVersion uint8      `msgpack:"schema_version"`
	PayloadType   uint8      `msgpack:"payload_type"`
	NodeID        string     `msgpack:"node_id"`
	TimestampUnix int64      `msgpack:"timestamp_unix"`
	Status        string     `msgpack:"status"`
	Metrics       USEMetrics `msgpack:"metrics"`
}

// DriftPayload reports configuration drift detected by ansible-playbook --check.
type DriftPayload struct {
	SchemaVersion uint8    `msgpack:"schema_version"`
	PayloadType   uint8    `msgpack:"payload_type"`
	NodeID        string   `msgpack:"node_id"`
	TimestampUnix int64    `msgpack:"timestamp_unix"`
	Status        string   `msgpack:"status"`
	TasksChanged  []string `msgpack:"tasks_changed"`
}

func assert(condition bool, message string) {
	if !condition {
		panic("wire: assertion failed: " + message)
	}
}

// EncodeHeartbeat serializes a HeartbeatPayload to MessagePack bytes.
// Sets SchemaVersion and PayloadType before encoding.
func EncodeHeartbeat(p HeartbeatPayload) ([]byte, error) {
	assert(p.NodeID != "", "node_id must not be empty")
	assert(p.TimestampUnix > 0, "timestamp_unix must be positive")

	p.SchemaVersion = SchemaVersionCurrent
	p.PayloadType = PayloadTypeHeartbeat

	data, err := msgpack.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode heartbeat: %w", err)
	}
	return data, nil
}

// EncodeDrift serializes a DriftPayload to MessagePack bytes.
func EncodeDrift(p DriftPayload) ([]byte, error) {
	assert(p.NodeID != "", "node_id must not be empty")
	assert(p.TimestampUnix > 0, "timestamp_unix must be positive")

	p.SchemaVersion = SchemaVersionCurrent
	p.PayloadType = PayloadTypeDrift

	data, err := msgpack.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode drift: %w", err)
	}
	return data, nil
}

// DecodeHeartbeat deserializes MessagePack bytes into a HeartbeatPayload.
// Returns an error (not a panic) for malformed or version-mismatched data.
func DecodeHeartbeat(data []byte) (HeartbeatPayload, error) {
	assert(len(data) > 0, "data must not be empty")

	var p HeartbeatPayload
	if err := msgpack.Unmarshal(data, &p); err != nil {
		return HeartbeatPayload{}, fmt.Errorf("decode heartbeat: %w", err)
	}
	if p.SchemaVersion != SchemaVersionCurrent {
		return HeartbeatPayload{}, fmt.Errorf("unsupported schema version: %d", p.SchemaVersion)
	}
	if p.NodeID == "" {
		return HeartbeatPayload{}, fmt.Errorf("decoded node_id is empty")
	}
	return p, nil
}

// DecodeDrift deserializes MessagePack bytes into a DriftPayload.
func DecodeDrift(data []byte) (DriftPayload, error) {
	assert(len(data) > 0, "data must not be empty")

	var p DriftPayload
	if err := msgpack.Unmarshal(data, &p); err != nil {
		return DriftPayload{}, fmt.Errorf("decode drift: %w", err)
	}
	if p.SchemaVersion != SchemaVersionCurrent {
		return DriftPayload{}, fmt.Errorf("unsupported schema version: %d", p.SchemaVersion)
	}
	if p.NodeID == "" {
		return DriftPayload{}, fmt.Errorf("decoded node_id is empty")
	}
	return p, nil
}

// Sign computes HMAC-SHA256(secret_bytes, payload_bytes) and returns a 32-byte signature.
func Sign(secret_bytes, payload_bytes []byte) [HMACSize]byte {
	assert(len(secret_bytes) > 0, "secret_bytes must not be empty")
	assert(len(payload_bytes) > 0, "payload_bytes must not be empty")

	mac := hmac.New(sha256.New, secret_bytes)
	mac.Write(payload_bytes)

	var sig [HMACSize]byte
	copy(sig[:], mac.Sum(nil))
	return sig
}

// Verify returns true if HMAC-SHA256(secret_bytes, payload_bytes) matches sig_bytes.
// Uses constant-time comparison to prevent timing side-channels.
func Verify(secret_bytes, payload_bytes, sig_bytes []byte) bool {
	assert(len(secret_bytes) > 0, "secret_bytes must not be empty")
	assert(len(payload_bytes) > 0, "payload_bytes must not be empty")
	assert(len(sig_bytes) == HMACSize, "sig_bytes length must equal HMACSize")

	expected := Sign(secret_bytes, payload_bytes)
	return hmac.Equal(expected[:], sig_bytes)
}

// PeekNodeID extracts only the node_id field from raw MessagePack bytes
// without a full decode. Used by hub ingest to look up the HMAC secret
// before signature verification.
func PeekNodeID(data []byte) (string, error) {
	assert(len(data) > 0, "data must not be empty")

	var peek struct {
		SchemaVersion uint8  `msgpack:"schema_version"`
		PayloadType   uint8  `msgpack:"payload_type"`
		NodeID        string `msgpack:"node_id"`
	}
	if err := msgpack.Unmarshal(data, &peek); err != nil {
		return "", fmt.Errorf("peek node_id: %w", err)
	}
	if peek.NodeID == "" {
		return "", fmt.Errorf("node_id is empty in payload")
	}
	return peek.NodeID, nil
}

// PeekPayloadType reads schema_version and payload_type from raw bytes.
// Returns an error if schema_version is unknown.
func PeekPayloadType(data []byte) (uint8, error) {
	assert(len(data) > 0, "data must not be empty")

	var peek struct {
		SchemaVersion uint8 `msgpack:"schema_version"`
		PayloadType   uint8 `msgpack:"payload_type"`
	}
	if err := msgpack.Unmarshal(data, &peek); err != nil {
		return 0, fmt.Errorf("peek payload_type: %w", err)
	}
	if peek.SchemaVersion != SchemaVersionCurrent {
		return 0, fmt.Errorf("unsupported schema version: %d", peek.SchemaVersion)
	}
	return peek.PayloadType, nil
}

// BuildAckFrame returns a minimal hub→agent acknowledgement frame.
// Format: [4-byte length=1][PayloadTypeAck]. No HMAC — TLS authenticates the hub.
func BuildAckFrame() []byte {
	frame := make([]byte, FrameHeaderSize+1)
	binary.BigEndian.PutUint32(frame[:FrameHeaderSize], 1)
	frame[FrameHeaderSize] = PayloadTypeAck
	return frame
}

// BuildFrame assembles a length-prefixed wire frame:
// [4-byte big-endian uint32 = len(payload)+HMACSize][payload_bytes][HMAC-SHA256]
func BuildFrame(secret_bytes, payload_bytes []byte) []byte {
	assert(len(secret_bytes) > 0, "secret_bytes must not be empty")
	assert(len(payload_bytes) > 0, "payload_bytes must not be empty")

	sig := Sign(secret_bytes, payload_bytes)
	body_len_bytes := len(payload_bytes) + HMACSize

	frame := make([]byte, FrameHeaderSize+body_len_bytes)
	binary.BigEndian.PutUint32(frame[:FrameHeaderSize], uint32(body_len_bytes))
	copy(frame[FrameHeaderSize:FrameHeaderSize+len(payload_bytes)], payload_bytes)
	copy(frame[FrameHeaderSize+len(payload_bytes):], sig[:])
	return frame
}

// SplitFrame separates the payload bytes from the HMAC signature in a body slice.
// body_bytes must be the bytes after the 4-byte length prefix.
// Returns an error if body_bytes is too short.
func SplitFrame(body_bytes []byte) (payload_bytes, sig_bytes []byte, err error) {
	assert(len(body_bytes) > 0, "body_bytes must not be empty")

	if len(body_bytes) <= HMACSize {
		return nil, nil, fmt.Errorf("frame body too short: %d bytes (minimum %d)", len(body_bytes), HMACSize+1)
	}
	split_at := len(body_bytes) - HMACSize
	payload_bytes = body_bytes[:split_at]
	sig_bytes = body_bytes[split_at:]
	return payload_bytes, sig_bytes, nil
}
