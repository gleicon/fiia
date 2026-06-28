package main

import (
	"context"
	"flag"
	"log"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/agent/audit"
	"github.com/gleicon/fiia/internal/agent/heartbeat"
	"github.com/gleicon/fiia/internal/agent/sdnotify"
	"github.com/gleicon/fiia/internal/agent/transport"
	"github.com/gleicon/fiia/internal/wire"
)

const default_config_path = "/etc/fiia/agent.toml"

func main() {
	config_path := flag.String("config", default_config_path, "path to agent TOML config")
	flag.Parse()

	if *config_path == "" {
		log.Fatal("agent: -config path must not be empty")
	}

	cfg, err := agentcfg.Load(*config_path)
	if err != nil {
		log.Fatalf("agent: load config %q: %v", *config_path, err)
	}

	tr, err := transport.New(cfg)
	if err != nil {
		log.Fatalf("agent: init transport: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sdnotify.Notify("READY=1")
	log.Printf("agent: started (node_id=%s)", cfg.NodeID)

	go heartbeat.Run(ctx, cfg, tr)

	if cfg.AnsiblePlaybookPath != "" {
		if err := audit.Probe(cfg); err != nil {
			log.Printf("agent: ansible probe failed — audit disabled: %v", err)
			tr.SendAuditResult(wire.DriftPayload{
				NodeID:        cfg.NodeID,
				TimestampUnix: time.Now().Unix(),
				Status:        "AUDIT_ERROR",
			})
		} else {
			go runAuditLoop(ctx, cfg, tr)
		}
	}

	sig_ch := make(chan os.Signal, 1)
	signal.Notify(sig_ch, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sig_ch

	log.Printf("agent: received signal %s, shutting down", sig)
	cancel()
}

func runAuditLoop(ctx context.Context, cfg *agentcfg.AgentConfig, tr *transport.Transport) {
	for {
		base_sec := cfg.AuditIntervalSec
		jitter_sec := rand.IntN(cfg.AuditJitterMaxSec + 1)
		sleep_duration := time.Duration(base_sec+jitter_sec) * time.Second

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep_duration):
		}

		p, ok := audit.Run(cfg)
		if !ok {
			continue
		}
		tr.SendAuditResult(p)
	}
}
