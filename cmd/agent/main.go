package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gleicon/fiia/internal/agent/audit"
	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	agentotel "github.com/gleicon/fiia/internal/agent/otel"
	"github.com/gleicon/fiia/internal/agent/sdnotify"
)

const (
	default_config_path   = "/etc/fiia/agent.toml"
	watchdog_interval_sec = 25
	otel_shutdown_timeout = 5 * time.Second
)

// otlpEndpoint resolves the OTLP/HTTP endpoint: flag wins, then config,
// then the standard OTEL_EXPORTER_OTLP_ENDPOINT environment variable.
// Empty means stdout export (no collector needed).
func otlpEndpoint(flag_val, cfg_val string) string {
	if flag_val != "" {
		return flag_val
	}
	if cfg_val != "" {
		return cfg_val
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
}

func main() {
	config_path := flag.String("config", default_config_path, "path to agent TOML config")
	check_mode := flag.Bool("check", false, "one-shot manifest check and exit (emits OTel, prints result)")
	write_manifest := flag.Bool("write-manifest", false, "generate a manifest from live state and exit")
	manifest_flag := flag.String("manifest", "", "manifest path (check target, or write-manifest destination)")
	format_flag := flag.String("format", "text", "check output format: text or json")
	node_flag := flag.String("node-id", "", "node id (defaults to config NodeID or hostname)")
	files_flag := flag.String("files", "", "write-manifest: comma-separated absolute file paths to track")
	packages_flag := flag.String("packages", "", "write-manifest: comma-separated packages (name or name=version)")
	services_flag := flag.String("services", "", "write-manifest: comma-separated service names to track")
	snapshot_flag := flag.Bool("snapshot", false, "write-manifest: also record full installed-package and active-service lists")
	scan_flag := flag.String("scan-playbook", "", "comma-separated playbook paths to scan for managed files (feeds -write-manifest, or prints the list alone)")
	otlp_flag := flag.String("otlp-endpoint", "", "OTLP/HTTP endpoint (default: OTEL_EXPORTER_OTLP_ENDPOINT env, else stdout)")
	remediate_flag := flag.Bool("remediate", false, "enforce the manifest (remove unauthorized packages/services, fix declared service states). WARNING: this covers ONLY the packages & services recorded in the manifest snapshot — it does NOT restore users, cron, firewall, sysctl, or any other state that Ansible/IaC manages. OFF by default; re-running the provisioning playbook is the only complete fix. Files are never auto-restored.")
	record_begin := flag.Bool("record-begin", false, "record: index the file tree under -dirs into -data before a provisioning run")
	record_end := flag.Bool("record-end", false, "record: diff -data against a fresh index and merge the changed files into -manifest (the ansible inventory)")
	data_flag := flag.String("data", "", "record: index path (record-begin writes it, record-end reads it)")
	dirs_flag := flag.String("dirs", "", "record: comma-separated root dirs to index")
	flag.Parse()

	if *check_mode && *write_manifest {
		fmt.Fprintln(os.Stderr, "agent: -check and -write-manifest are mutually exclusive")
		os.Exit(2)
	}
	if *scan_flag != "" && !*write_manifest {
		os.Exit(runScan(*scan_flag))
	}
	if *write_manifest {
		os.Exit(runWriteManifest(*manifest_flag, *files_flag, *packages_flag, *services_flag, *snapshot_flag, *scan_flag))
	}
	if *record_begin && *record_end {
		fmt.Fprintln(os.Stderr, "agent: -record-begin and -record-end are mutually exclusive")
		os.Exit(2)
	}
	if *record_begin {
		os.Exit(runRecordBegin(*data_flag, *dirs_flag))
	}
	if *record_end {
		os.Exit(runRecordEnd(*manifest_flag, *data_flag, *dirs_flag))
	}
	if *check_mode {
		os.Exit(runCheck(*config_path, *manifest_flag, *format_flag, *node_flag, *otlp_flag, *remediate_flag))
	}

	os.Exit(runDaemon(*config_path, *node_flag, *otlp_flag))
}

// resolveNodeID returns the effective node id: flag, config, hostname, fallback.
func resolveNodeID(flag_val, cfg_val string) string {
	if flag_val != "" {
		return flag_val
	}
	if cfg_val != "" {
		return cfg_val
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		return hn
	}
	return "localhost"
}

// fatal logs a startup error and exits. The agent runs under systemd, so
// failures are visible through the service log.
func fatal(msg string, err error) {
	if err != nil {
		slog.Error(msg, "err", err.Error())
	} else {
		slog.Error(msg)
	}
	os.Exit(1)
}

// runDaemon is the long-running mode: heartbeat liveness plus periodic
// manifest checks, all emitted to OTel. Alerting happens downstream.
func runDaemon(config_path, node_flag, otlp_flag string) int {
	if config_path == "" {
		fatal("agent: -config path must not be empty", nil)
	}
	cfg, err := agentcfg.Load(config_path)
	if err != nil {
		fatal(fmt.Sprintf("agent: load config %q", config_path), err)
	}
	cfg.NodeID = resolveNodeID(node_flag, cfg.NodeID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	otel, shutdown, err := agentotel.Setup(ctx, otlpEndpoint(otlp_flag, cfg.OTLPEndpoint), cfg.NodeID)
	if err != nil {
		fatal("agent: otel setup", err)
	}
	defer func() {
		shut_ctx, shut_cancel := context.WithTimeout(context.Background(), otel_shutdown_timeout)
		defer shut_cancel()
		if err := shutdown(shut_ctx); err != nil {
			slog.Warn("agent: otel shutdown", "err", err.Error())
		}
	}()

	sdnotify.Notify("READY=1")
	slog.Info("agent: started", "node_id", cfg.NodeID, "manifest", cfg.ManifestPath)

	if err := audit.ProbeManifest(cfg); err != nil {
		slog.Warn("agent: manifest probe failed, checks disabled", "err", err.Error())
		// Emit periodic CHECK_ERROR verdicts so backends alert instead of
		// seeing a silent node that never produces a drift verdict.
		go runErrorLoop(ctx, cfg, otel, err)
	} else {
		go runAuditLoop(ctx, cfg, otel)
	}
	go runHeartbeatLoop(ctx, cfg, otel)

	sig_ch := make(chan os.Signal, 1)
	signal.Notify(sig_ch, syscall.SIGTERM, syscall.SIGINT)
	sig := <-sig_ch

	slog.Info("agent: received signal, shutting down", "signal", sig.String())
	cancel()
	return 0
}

// runHeartbeatLoop emits fiia.alive on the heartbeat interval and pings the
// systemd watchdog. Backends alert on a missing fiia.alive series.
func runHeartbeatLoop(ctx context.Context, cfg *agentcfg.AgentConfig, otel *agentotel.Handle) {
	heartbeat_timer := time.NewTimer(0)
	defer heartbeat_timer.Stop()
	watchdog_ticker := time.NewTicker(watchdog_interval_sec * time.Second)
	defer watchdog_ticker.Stop()

	interval := time.Duration(cfg.HeartbeatIntervalSec) * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-watchdog_ticker.C:
			sdnotify.Notify("WATCHDOG=1")
		case <-heartbeat_timer.C:
			otel.EmitAlive(ctx)
			heartbeat_timer.Reset(interval)
		}
	}
}

// waitTick sleeps one audit interval (plus splay jitter) and reports whether
// to continue (false when the context is cancelled). Shared by the audit and
// error loops so the interval/sleep logic lives in one place.
func waitTick(ctx context.Context, cfg *agentcfg.AgentConfig) bool {
	jitter_sec := rand.IntN(cfg.AuditJitterMaxSec + 1)
	sleep_duration := time.Duration(cfg.AuditIntervalSec+jitter_sec) * time.Second
	select {
	case <-ctx.Done():
		return false
	case <-time.After(sleep_duration):
		return true
	}
}

// runAuditLoop verifies the manifest on the audit interval with splay jitter
// and emits every verdict to OTel.
func runAuditLoop(ctx context.Context, cfg *agentcfg.AgentConfig, otel *agentotel.Handle) {
	for {
		if !waitTick(ctx, cfg) {
			return
		}

		result, ok := audit.RunManifest(cfg)
		if !ok {
			continue
		}
		// Explicitly authorized auto-remediation (remediate=true in the config,
		// OFF by default). Enforces the manifest for packages/services; file
		// deviations remain the provisioning playbook's job (IaS).
		if cfg.Remediate && result.Status == audit.StatusDriftDetected {
			if rem, err := audit.RemediatePath(cfg.ManifestPath); err == nil && !rem.Empty() {
				slog.Info(fmt.Sprintf("remediate: %s", rem.String()))
			}
		}
		otel.EmitCheck(ctx, result)
		if result.Status == audit.StatusOK {
			slog.Info("audit: clean")
		} else {
			slog.Info(fmt.Sprintf("audit: %s deviations=%d", result.Status, len(result.Deviations)))
		}
	}
}

// runErrorLoop emits CHECK_ERROR verdicts on the audit interval while the
// manifest cannot be read (e.g. permission denied). Unlike a silent disable,
// this keeps the node visible to alerting: fiia.drift.status=2 with a
// remediation hint.
func runErrorLoop(ctx context.Context, cfg *agentcfg.AgentConfig, otel *agentotel.Handle, probeErr error) {
	hint := manifestPermissionHint(probeErr)
	for {
		if !waitTick(ctx, cfg) {
			return
		}

		result := audit.CheckResult{
			NodeID:        cfg.NodeID,
			TimestampUnix: time.Now().Unix(),
			Status:        audit.StatusCheckError,
			Deviations:    []string{fmt.Sprintf("manifest:unreadable:%v%s", probeErr, hint)},
		}
		otel.EmitCheck(ctx, result)
		slog.Info(fmt.Sprintf("audit: %s (%v)%s", result.Status, probeErr, hint))
	}
}

// manifestPermissionHint returns a remediation hint when the probe error is a
// permission problem, so operators know no OS change is required beyond fixing
// manifest ownership (or granting D-Bus access for service checks).
func manifestPermissionHint(err error) string {
	if err != nil && (os.IsPermission(err) || strings.Contains(err.Error(), "permission denied")) {
		return " — grant the service user read access to the manifest (chown to the agent user, e.g. 'chown fiia:root /etc/fiia/manifest.json && chmod 0400') or run the agent as root"
	}
	return ""
}

// runScan prints files managed by the given playbooks (one per line) plus
// any skipped/unresolved items on stderr. Exit 0 on success, 2 on scan error.
func runScan(scan_flag string) int {
	paths := splitCSV(scan_flag)
	if len(paths) == 0 {
		fmt.Fprintln(os.Stderr, "agent: -scan-playbook needs at least one path")
		return 2
	}
	files, warnings, err := audit.ScanPlaybooks(paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: scan: %v\n", err)
		return 2
	}
	for _, f := range files {
		fmt.Println(f)
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	return 0
}

// runWriteManifest implements manifest generation: hash declared files, record
// installed package versions and service states, write the JSON manifest.
// Exit codes: 0 = written, 2 = usage or write error. Warnings (missing files or
// packages) are printed but still exit 0.
func runWriteManifest(dest, files_flag, packages_flag, services_flag string, snapshot bool, scan_flag string) int {
	if dest == "" {
		fmt.Fprintln(os.Stderr, "agent: -manifest is required with -write-manifest")
		return 2
	}
	files := splitCSV(files_flag)
	services := splitCSV(services_flag)

	if scan_flag != "" {
		scanned, warnings, err := audit.ScanPlaybooks(splitCSV(scan_flag))
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: scan: %v\n", err)
			return 2
		}
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "warning: %s\n", w)
		}
		files = audit.Dedupe(append(files, scanned...))
	}

	var pkgs []audit.PackageSpec
	for _, s := range splitCSV(packages_flag) {
		spec, err := audit.ParsePackageSpec(s)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			return 2
		}
		pkgs = append(pkgs, spec)
	}

	warnings, changed, err := audit.GenerateManifest(dest, files, pkgs, services, snapshot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: write manifest: %v\n", err)
		return 2
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	if changed {
		fmt.Printf("wrote %s (changed, files=%d packages=%d services=%d)\n", dest, len(files), len(pkgs), len(services))
	} else {
		fmt.Printf("unchanged %s (files=%d packages=%d services=%d)\n", dest, len(files), len(pkgs), len(services))
	}
	return 0
}

// runRecordBegin saves a file index of the roots before a provisioning run.
func runRecordBegin(data_flag, dirs_flag string) int {
	roots := splitCSV(dirs_flag)
	if data_flag == "" || len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "agent: -record-begin needs -data <index> and -dirs <roots>")
		return 2
	}
	n, err := audit.RecordBegin(data_flag, roots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: record-begin: %v\n", err)
		return 2
	}
	fmt.Printf("recorded %d files to %s\n", n, data_flag)
	return 0
}

// runRecordEnd diffs the pre-run index against the current file tree, merges
// the changed files into the manifest, and refreshes the package/service
// snapshots. The result is the ansible inventory for the node.
func runRecordEnd(manifest_flag, data_flag, dirs_flag string) int {
	roots := splitCSV(dirs_flag)
	if manifest_flag == "" || data_flag == "" || len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "agent: -record-end needs -manifest <path>, -data <index>, and -dirs <roots>")
		return 2
	}
	added, removed, err := audit.RecordEnd(manifest_flag, data_flag, roots)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: record-end: %v\n", err)
		return 2
	}
	fmt.Printf("wrote %s (files added=%d removed=%d)\n", manifest_flag, added, removed)
	return 0
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// resolveCheckInputs determines the manifest path, node id, and OTLP endpoint
// for a check. With -manifest the config is optional and only fills in unset
// node id / endpoint; without it the manifest must come from the config file.
func resolveCheckInputs(config_path, manifest_flag, node_flag, otlp_flag string) (string, string, string, error) {
	manifest_path := manifest_flag
	node_id := node_flag
	endpoint := otlp_flag

	if manifest_path != "" {
		if config_path != "" {
			if cfg, err := agentcfg.Load(config_path); err == nil {
				if node_id == "" {
					node_id = cfg.NodeID
				}
				if endpoint == "" {
					endpoint = cfg.OTLPEndpoint
				}
			}
		}
	} else if config_path != "" {
		cfg, err := agentcfg.Load(config_path)
		if err != nil {
			return "", "", "", fmt.Errorf("load config %q: %w", config_path, err)
		}
		manifest_path = cfg.ManifestPath
		if node_id == "" {
			node_id = cfg.NodeID
		}
		if endpoint == "" {
			endpoint = cfg.OTLPEndpoint
		}
	}

	if manifest_path == "" {
		return "", "", "", errors.New("no manifest to check (use -manifest or set manifest_path in config)")
	}
	return manifest_path, node_id, endpoint, nil
}

// runCheck runs one manifest check, emits it to OTel, and prints the verdict.
// Exit codes: 0 = no drift, 1 = drift detected, 2 = usage/config/operational error.
func runCheck(config_path, manifest_flag, format_flag, node_flag, otlp_flag string, remediate bool) int {
	if format_flag != "text" && format_flag != "json" {
		fmt.Fprintf(os.Stderr, "agent: -format must be text or json, got %q\n", format_flag)
		return 2
	}

	manifest_path, node_id, endpoint, err := resolveCheckInputs(config_path, manifest_flag, node_flag, otlp_flag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		return 2
	}
	node_id = resolveNodeID(node_id, "")

	result, ok := audit.CheckLocal(manifest_path, node_id)
	if !ok {
		if _, statErr := os.Stat(manifest_path); statErr == nil {
			// File exists but could not be read — a permission problem, not a
			// missing manifest.
			fmt.Fprintf(os.Stderr, "agent: manifest %q exists but could not be read — grant the service user read access (chown/chmod) or run as root\n", manifest_path)
			return 2
		}
		fmt.Fprintln(os.Stderr, "agent: no manifest to check (use -manifest or set manifest_path in config)")
		return 2
	}

	if remediate {
		result = runRemediation(manifest_path, node_id, result)
	}

	emitOTel(result, endpoint, node_id)
	printResult(result, manifest_path, format_flag)

	switch result.Status {
	case audit.StatusOK:
		return 0
	case audit.StatusDriftDetected:
		return 1
	default:
		return 2
	}
}

// runRemediation enforces the manifest (explicitly authorized). Packages and
// services are corrected by the agent; file deviations are reported as needing
// the provisioning playbook (IaS restores file content). Returns the re-checked
// result after the enforcement pass.
func runRemediation(manifest_path, node_id string, before audit.CheckResult) audit.CheckResult {
	// Big scope warning: remediation only enforces what the manifest snapshot
	// records (packages + services). Config-management state that the manifest
	// does not capture (users, cron, firewall, sysctl, …) is untouched and must
	// be restored by re-running the provisioning playbook (IaS).
	fmt.Println("WARNING: remediation enforces ONLY packages & services recorded in the manifest snapshot.")
	fmt.Println("         It does NOT restore users, cron, firewall, sysctl, or any other state that")
	fmt.Println("         Ansible/IaC manages — re-running the provisioning playbook is the complete fix.")

	m, err := audit.LoadFile(manifest_path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: remediate: load manifest: %v\n", err)
		return before
	}
	rem := audit.Remediate(m)
	if !rem.Empty() {
		fmt.Printf("remediate: %s\n", rem.String())
	}
	if after, ok := audit.CheckLocal(manifest_path, node_id); ok {
		if after.Status == audit.StatusOK {
			fmt.Println("remediate: manifest now satisfied")
		}
		for _, d := range after.Deviations {
			if strings.HasPrefix(d, "file:") {
				fmt.Println("remediate: file deviations remain — restore file content by re-running the provisioning playbook (IaS)")
				break
			}
		}
		return after
	}
	return before
}

// emitOTel sends one check verdict to the OTel pipeline. Failures are logged
// but never change the exit code: the printed verdict is the contract.
func emitOTel(result audit.CheckResult, endpoint, node_id string) {
	ctx, cancel := context.WithTimeout(context.Background(), otel_shutdown_timeout)
	defer cancel()

	otel, shutdown, err := agentotel.Setup(ctx, otlpEndpoint(endpoint, ""), node_id)
	if err != nil {
		slog.Warn("agent: otel setup", "err", err.Error())
		return
	}
	otel.EmitCheck(ctx, result)
	if err := shutdown(ctx); err != nil {
		slog.Warn("agent: otel shutdown", "err", err.Error())
	}
}

func printResult(result audit.CheckResult, manifest_path, format_flag string) {
	if format_flag == "json" {
		view := map[string]any{
			"node_id":   result.NodeID,
			"status":    result.Status,
			"timestamp": result.TimestampUnix,
			"deviations": func() []string {
				if result.Deviations == nil {
					return []string{}
				}
				return result.Deviations
			}(),
		}
		if manifest, err := audit.LoadFile(manifest_path); err == nil {
			view["files_checked"] = len(manifest.Files)
			view["packages_checked"] = len(manifest.Packages)
			view["services_checked"] = len(manifest.Services)
		}
		out, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent: encode result: %v\n", err)
			return
		}
		fmt.Println(string(out))
		return
	}

	switch result.Status {
	case audit.StatusOK:
		if manifest, err := audit.LoadFile(manifest_path); err == nil {
			fmt.Printf("OK: no drift (files=%d packages=%d services=%d)\n",
				len(manifest.Files), len(manifest.Packages), len(manifest.Services))
		} else {
			fmt.Println("OK: no drift")
		}
	case audit.StatusDriftDetected:
		fmt.Printf("DRIFT: %d deviation(s)\n", len(result.Deviations))
		printDeviations(result.Deviations)
	default:
		fmt.Printf("%s\n", result.Status)
		printDeviations(result.Deviations)
	}
}

// printDeviations prints the deviation list, one indented line each.
func printDeviations(devs []string) {
	for _, d := range devs {
		fmt.Printf("  - %s\n", d)
	}
}
