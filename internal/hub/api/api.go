package api

import (
	"encoding/json"
	"log"
	"net"
	"net/http"

	"github.com/gleicon/fiia/internal/hub/command"
	"github.com/gleicon/fiia/internal/hub/store"
)

func assert(condition bool, message string) {
	if !condition {
		panic("hub/api: assertion failed: " + message)
	}
}

// Server exposes the hub REST API.
type Server struct {
	store store.Store
	cmdq  *command.Queue // may be nil
}

// New creates an API Server. cmdq may be nil (command endpoints return 503).
func New(s store.Store, cmdq *command.Queue) *Server {
	assert(s != nil, "store must not be nil")
	return &Server{store: s, cmdq: cmdq}
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

// ServeListener serves the API on the given listener.
func (srv *Server) ServeListener(ln net.Listener) error {
	assert(ln != nil, "ln must not be nil")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes", srv.listNodes)
	mux.HandleFunc("GET /nodes/{id}/status", srv.getNodeStatus)
	mux.HandleFunc("GET /nodes/{id}/drift", srv.getNodeDrift)
	mux.HandleFunc("GET /alerts", srv.listAlerts)
	mux.HandleFunc("POST /nodes/{id}/audit_now", srv.postAuditNow)
	mux.HandleFunc("POST /nodes/{id}/config", srv.postConfig)
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
	if node_id == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
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
	if node_id == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
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
	if node_id == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
		return
	}
	if srv.cmdq == nil {
		http.Error(w, "command queue not available", http.StatusServiceUnavailable)
		return
	}
	srv.cmdq.Enqueue(node_id, "audit_now")
	log.Printf("api: enqueued audit_now for node %s", node_id)
	w.WriteHeader(http.StatusAccepted)
}

// postConfig enqueues a "config_update" command for the node.
// Body: JSON {"playbook_path": "...", "interval_sec": N}
func (srv *Server) postConfig(w http.ResponseWriter, r *http.Request) {
	node_id := r.PathValue("id")
	if node_id == "" {
		http.Error(w, "node id required", http.StatusBadRequest)
		return
	}
	if srv.cmdq == nil {
		http.Error(w, "command queue not available", http.StatusServiceUnavailable)
		return
	}
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
	srv.cmdq.Enqueue(node_id, "config_update")
	log.Printf("api: enqueued config_update for node %s (playbook=%q interval=%d)", node_id, req.PlaybookPath, req.IntervalSec)
	w.WriteHeader(http.StatusAccepted)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
