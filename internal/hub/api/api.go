package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gleicon/fiia/internal/hub/command"
	"github.com/gleicon/fiia/internal/hub/store"
)

const (
	node_id_max_len    = 256
	config_body_max    = 4096
	enroll_secret_size = 32
)

func assert(condition bool, message string) {
	if !condition {
		panic("hub/api: assertion failed: " + message)
	}
}

func validNodeID(w http.ResponseWriter, node_id string) bool {
	if node_id == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
		return false
	}
	if len(node_id) > node_id_max_len {
		http.Error(w, "node id too long", http.StatusBadRequest)
		return false
	}
	return true
}

// Server exposes the hub REST API and optional Prometheus metrics on one port.
type Server struct {
	store           store.Store
	cmdq            *command.Queue     // may be nil
	promGather      prometheus.Gatherer // may be nil; if set, /metrics and /healthz are served
	enrollmentToken string             // may be ""; if set, POST /nodes/{id}/enroll is registered
}

// New creates an API Server. cmdq may be nil (command endpoints return 503).
func New(s store.Store, cmdq *command.Queue) *Server {
	assert(s != nil, "store must not be nil")
	return &Server{store: s, cmdq: cmdq}
}

// WithMetrics enables /metrics and /healthz routes on the same listener.
// Pass prometheus.DefaultGatherer in production; nil disables the routes.
func (srv *Server) WithMetrics(g prometheus.Gatherer) *Server {
	srv.promGather = g
	return srv
}

// WithEnrollmentToken enables POST /nodes/{id}/enroll, protected by Bearer token auth.
// The endpoint generates a 32-byte HMAC secret and stores it via SetNodeSecret.
// Pass "" to disable the endpoint (default).
func (srv *Server) WithEnrollmentToken(token string) *Server {
	srv.enrollmentToken = token
	return srv
}

// Serve creates a TCP listener on addr and calls ServeListener.
func (srv *Server) Serve(addr string) error {
	assert(addr != "", "addr must not be empty")

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	log.Printf("api: serving on %s", addr)
	return srv.ServeListener(ln)
}

// ServeListener serves the API (and /metrics + /healthz if WithMetrics was called).
func (srv *Server) ServeListener(ln net.Listener) error {
	assert(ln != nil, "ln must not be nil")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", srv.listNodes)
	mux.HandleFunc("GET /nodes/{id}/status", srv.getNodeStatus)
	mux.HandleFunc("GET /nodes/{id}/drift", srv.getNodeDrift)
	mux.HandleFunc("GET /alerts", srv.listAlerts)
	mux.HandleFunc("POST /nodes/{id}/audit_now", srv.postAuditNow)
	mux.HandleFunc("POST /nodes/{id}/config", srv.postConfig)
	if srv.enrollmentToken != "" {
		mux.HandleFunc("POST /nodes/{id}/enroll", srv.postEnroll)
	}
	if srv.promGather != nil {
		mux.Handle("/metrics", promhttp.HandlerFor(srv.promGather, promhttp.HandlerOpts{}))
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	return http.Serve(ln, mux)
}

func (srv *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := srv.store.GetNodes()
	if err != nil {
		log.Printf("api: list nodes: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, nodes)
}

func (srv *Server) getNodeStatus(w http.ResponseWriter, r *http.Request) {
	node_id := r.PathValue("id")
	if !validNodeID(w, node_id) {
		return
	}
	node, err := srv.store.GetNode(node_id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, node)
}

func (srv *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := srv.store.GetAlerts()
	if err != nil {
		log.Printf("api: list alerts: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, alerts)
}

func (srv *Server) getNodeDrift(w http.ResponseWriter, r *http.Request) {
	node_id := r.PathValue("id")
	if !validNodeID(w, node_id) {
		return
	}
	events, err := srv.store.GetDriftEvents(node_id, 50)
	if err != nil {
		log.Printf("api: get drift events for %q: %v", node_id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}

// postAuditNow enqueues an "audit_now" command for the node.
// The command is delivered on the node's next heartbeat connection.
func (srv *Server) postAuditNow(w http.ResponseWriter, r *http.Request) {
	node_id := r.PathValue("id")
	if !validNodeID(w, node_id) {
		return
	}
	if srv.cmdq == nil {
		http.Error(w, "command queue not available", http.StatusServiceUnavailable)
		return
	}
	srv.cmdq.Enqueue(node_id, command.Entry{Command: "audit_now"})
	log.Printf("api: enqueued audit_now for node %s", node_id)
	w.WriteHeader(http.StatusAccepted)
}

// postConfig enqueues a "config_update" command for the node.
// Body: JSON {"playbook_path": "...", "interval_sec": N}
func (srv *Server) postConfig(w http.ResponseWriter, r *http.Request) {
	node_id := r.PathValue("id")
	if !validNodeID(w, node_id) {
		return
	}
	if srv.cmdq == nil {
		http.Error(w, "command queue not available", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, config_body_max)
	var req struct {
		PlaybookPath string `json:"playbook_path"`
		IntervalSec  int    `json:"interval_sec"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.PlaybookPath == "" && req.IntervalSec == 0 {
		http.Error(w, "at least one of playbook_path or interval_sec required", http.StatusBadRequest)
		return
	}
	srv.cmdq.Enqueue(node_id, command.Entry{
		Command:      "config_update",
		PlaybookPath: req.PlaybookPath,
		IntervalSec:  req.IntervalSec,
	})
	log.Printf("api: enqueued config_update for node %s (playbook=%q interval=%d)", node_id, req.PlaybookPath, req.IntervalSec)
	w.WriteHeader(http.StatusAccepted)
}

// postEnroll generates a fresh HMAC secret for node_id and stores it.
// Requires Authorization: Bearer <enrollment_token> header.
// Returns {"node_id": "...", "secret": "<hex>"}.
func (srv *Server) postEnroll(w http.ResponseWriter, r *http.Request) {
	node_id := r.PathValue("id")
	if !validNodeID(w, node_id) {
		return
	}

	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) || auth[len(prefix):] != srv.enrollmentToken {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	secret := make([]byte, enroll_secret_size)
	if _, err := rand.Read(secret); err != nil {
		log.Printf("api: enroll: generate secret for %q: %v", node_id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := srv.store.SetNodeSecret(node_id, secret); err != nil {
		log.Printf("api: enroll: set secret for %q: %v", node_id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	log.Printf("api: enrolled node %s", node_id)
	writeJSON(w, map[string]string{
		"node_id": node_id,
		"secret":  hex.EncodeToString(secret),
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
