package main

import (
	"crypto/tls"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"

	hubapi "github.com/gleicon/fiia/internal/hub/api"
	hubcfg "github.com/gleicon/fiia/internal/hub/config"
	"github.com/gleicon/fiia/internal/hub/command"
	"github.com/gleicon/fiia/internal/hub/ingest"
	"github.com/gleicon/fiia/internal/hub/inventory"
	"github.com/gleicon/fiia/internal/hub/metrics"
	"github.com/gleicon/fiia/internal/hub/registry"
	"github.com/gleicon/fiia/internal/hub/store"
)

const default_config_path = "/etc/fiia/hub.toml"

// seedNodeList is a repeatable flag for seeding dev node secrets.
type seedNodeList []string

func (s *seedNodeList) String() string  { return strings.Join(*s, ",") }
func (s *seedNodeList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	config_path := flag.String("config", default_config_path, "path to hub TOML config")
	var seed_nodes seedNodeList
	flag.Var(&seed_nodes, "seed-node", "dev only: node_id:hex_secret to pre-seed (repeatable)")
	flag.Parse()

	if *config_path == "" {
		log.Fatal("hub: -config path must not be empty")
	}

	cfg, err := hubcfg.Load(*config_path)
	if err != nil {
		log.Fatalf("hub: load config %q: %v", *config_path, err)
	}

	conn := cfg.DBPath
	if cfg.DBDriver == "postgres" {
		conn = cfg.DBDSN
	}
	db, err := store.Open(cfg.DBDriver, conn)
	if err != nil {
		log.Fatalf("hub: open store driver=%q: %v", cfg.DBDriver, err)
	}
	defer db.Close()

	for _, pair := range seed_nodes {
		node_id, secret_bytes, err := parseSeedNode(pair)
		if err != nil {
			log.Fatalf("hub: -seed-node %q: %v", pair, err)
		}
		if err := db.SetNodeSecret(node_id, secret_bytes); err != nil {
			log.Fatalf("hub: seed node %q: %v", node_id, err)
		}
		log.Printf("hub: seeded node %q", node_id)
	}

	reg := registry.New(db)

	tls_cfg, err := loadTLSConfig(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		log.Fatalf("hub: load TLS config: %v", err)
	}

	var drift_counter atomic.Int64
	cmdq := command.New()
	metrics_srv := metrics.New(reg, db, &drift_counter,
		prometheus.DefaultRegisterer, prometheus.DefaultGatherer)
	api_srv := hubapi.New(db, cmdq)
	ingest_l := ingest.New(tls_cfg, reg, db, &drift_counter, cmdq, cfg.RateLimitRPS, cfg.RateLimitBurst)

	stop_ch := make(chan struct{})

	go func() {
		if err := metrics_srv.Serve(cfg.MetricsAddr); err != nil {
			log.Printf("hub: metrics server error: %v", err)
		}
	}()
	go func() {
		if err := api_srv.Serve(cfg.APIAddr); err != nil {
			log.Printf("hub: api server error: %v", err)
		}
	}()
	go func() {
		if err := ingest_l.Serve(cfg.ListenAddr); err != nil {
			log.Printf("hub: ingest listener error: %v", err)
		}
	}()
	go reg.RunExpiry(stop_ch)

	if cfg.InventoryCSVPath != "" {
		csv_reader := inventory.NewCSVReader(cfg.InventoryCSVPath)
		go inventory.RunReconciler(csv_reader, reg, db, cfg.ReconcileIntervalSec, stop_ch)
	}

	log.Printf("hub: started (listen=%s metrics=%s api=%s)", cfg.ListenAddr, cfg.MetricsAddr, cfg.APIAddr)

	sig_ch := make(chan os.Signal, 1)
	signal.Notify(sig_ch, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sig_ch

	log.Printf("hub: received signal %s, shutting down", sig)
	close(stop_ch)
}

func parseSeedNode(pair string) (node_id string, secret_bytes []byte, err error) {
	parts := strings.SplitN(pair, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", nil, fmt.Errorf("want node_id:hex_secret")
	}
	secret_bytes, err = hex.DecodeString(parts[1])
	if err != nil {
		return "", nil, fmt.Errorf("decode hex secret: %w", err)
	}
	if len(secret_bytes) < 16 {
		return "", nil, fmt.Errorf("secret must be at least 16 bytes, got %d", len(secret_bytes))
	}
	return parts[0], secret_bytes, nil
}

func loadTLSConfig(cert_path, key_path string) (*tls.Config, error) {
	if cert_path == "" {
		return nil, fmt.Errorf("cert_path must not be empty")
	}
	if key_path == "" {
		return nil, fmt.Errorf("key_path must not be empty")
	}

	cert, err := tls.LoadX509KeyPair(cert_path, key_path)
	if err != nil {
		return nil, fmt.Errorf("load key pair cert=%q key=%q: %w", cert_path, key_path, err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}
