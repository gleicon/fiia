package inventory

import (
	"bufio"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gleicon/fiia/internal/hub/registry"
	"github.com/gleicon/fiia/internal/hub/store"
)

func assert(condition bool, message string) {
	if !condition {
		panic("hub/inventory: assertion failed: " + message)
	}
}

// Node represents a fleet node from the inventory source.
type Node struct {
	Hostname string
}

// InventoryReader is the interface for inventory sources.
// The CSV implementation is the MVP; NetBox REST is the planned upgrade path.
type InventoryReader interface {
	ListNodes() ([]Node, error)
}

// CSVReader reads inventory from a newline-delimited file (one hostname per line).
// Lines starting with '#' and empty lines are ignored.
type CSVReader struct {
	path string
}

// NewCSVReader creates a CSVReader for the file at path.
func NewCSVReader(path string) *CSVReader {
	assert(path != "", "path must not be empty")
	return &CSVReader{path: path}
}

// ListNodes reads and parses the CSV inventory file.
func (r *CSVReader) ListNodes() ([]Node, error) {
	assert(r.path != "", "path must not be empty")

	f, err := os.Open(r.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var nodes []Node
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept "hostname" or "hostname,alias" — use first field only.
		fields := strings.SplitN(line, ",", 2)
		hostname := strings.TrimSpace(fields[0])
		if hostname != "" {
			nodes = append(nodes, Node{Hostname: hostname})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return nodes, nil
}

// RunReconciler starts the inventory reconciler loop. Blocks until ctx channel is closed.
// On each tick, compares inventory against the heartbeat registry and flags absent nodes.
func RunReconciler(reader InventoryReader, reg *registry.Registry, s store.Store, interval_sec int, stop_ch <-chan struct{}) {
	assert(reader != nil, "reader must not be nil")
	assert(reg != nil, "registry must not be nil")
	assert(s != nil, "store must not be nil")
	assert(interval_sec > 0, "interval_sec must be positive")

	ticker := time.NewTicker(time.Duration(interval_sec) * time.Second)
	defer ticker.Stop()

	reconcile(reader, reg, s)
	for {
		select {
		case <-stop_ch:
			return
		case <-ticker.C:
			reconcile(reader, reg, s)
		}
	}
}

func reconcile(reader InventoryReader, reg *registry.Registry, s store.Store) {
	assert(reader != nil, "reader must not be nil")
	assert(reg != nil, "registry must not be nil")

	inventory_nodes, err := reader.ListNodes()
	if err != nil {
		log.Printf("inventory: list nodes: %v", err)
		return
	}

	registry_nodes := reg.GetAll()
	seen := make(map[string]struct{}, len(registry_nodes))
	for _, n := range registry_nodes {
		seen[n.NodeID] = struct{}{}
	}

	now_unix := time.Now().Unix()
	for _, inv_node := range inventory_nodes {
		if _, ok := seen[inv_node.Hostname]; ok {
			continue
		}
		if err := s.SetAlert(inv_node.Hostname, "UNINSTRUMENTED_SERVER", now_unix); err != nil {
			log.Printf("inventory: set UNINSTRUMENTED_SERVER alert for %q: %v", inv_node.Hostname, err)
		}
	}
}
