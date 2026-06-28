package api

import (
	"encoding/json"
	"log"
	"net"
	"net/http"

	"github.com/gleicon/fiia/internal/hub/store"
)

func assert(condition bool, message string) {
	if !condition {
		panic("hub/api: assertion failed: " + message)
	}
}

// Server exposes the hub REST API: /nodes, /nodes/{id}/status, /alerts.
type Server struct {
	store store.Store
}

// New creates an API Server backed by the given store.
func New(s store.Store) *Server {
	assert(s != nil, "store must not be nil")
	return &Server{store: s}
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}
