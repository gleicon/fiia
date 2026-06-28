package audit

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"time"

	agentcfg "github.com/gleicon/fiia/internal/agent/config"
	"github.com/gleicon/fiia/internal/wire"
)

// Ansible exit codes used in result interpretation.
const (
	exit_code_ok      = 0   // no drift
	exit_code_changed = 2   // one or more tasks would change
	exit_code_oom     = 137 // SIGKILL from OOM killer
)

// Drift payload status values written to DriftPayload.Status.
//
//   OK                     - ansible-playbook exited 0; baseline matches node state
//   DRIFT_DETECTED         - ansible-playbook exited 2; TasksChanged lists affected tasks
//   AUDIT_TIMEOUT          - audit_timeout_sec exceeded; playbook did not complete
//   AUDIT_ERROR            - ansible-playbook failed to start (exec error, not an exit code)
//   AUDIT_RESOURCE_EXCEEDED - ansible-playbook killed by OOM (exit 137)
//   AUDIT_EXIT_N           - ansible-playbook exited with code N not otherwise handled:
//                             1 = task/module error, 3 = unreachable hosts,
//                             4 = playbook syntax error, 5 = bad options (missing inventory),
//                             99 = interrupted
const (
	statusOK               = "OK"
	statusDriftDetected    = "DRIFT_DETECTED"
	statusTimeout          = "AUDIT_TIMEOUT"
	statusError            = "AUDIT_ERROR"
	statusResourceExceeded = "AUDIT_RESOURCE_EXCEEDED"
)

// Probe verifies that ansible-playbook is executable in the service environment.
// Call once at startup before the audit goroutine begins. Returns a non-nil error
// with ansible's output if the binary is missing or unusable.
func Probe(cfg *agentcfg.AgentConfig) error {
	assert(cfg != nil, "cfg must not be nil")

	if cfg.AnsiblePlaybookPath == "" {
		return nil
	}
	if err := writeAnsibleCfg(); err != nil {
		return fmt.Errorf("write ansible.cfg: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ansible-playbook", "--version")
	cmd.Env = append(cmd.Environ(),
		"ANSIBLE_LOCAL_TEMP=/var/lib/fiia/.ansible/tmp",
		"ANSIBLE_CONFIG="+ansibleCfgPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ansible-playbook --version failed: %w\n%s", err, out)
	}
	return nil
}

// Run executes ansible-playbook --check --diff on cfg.AnsiblePlaybookPath.
// Returns a DriftPayload encoding the result. Output is appended to cfg.DriftLogPath.
// If AnsiblePlaybookPath is empty, returns an empty payload and caller should skip sending.
func Run(cfg *agentcfg.AgentConfig) (wire.DriftPayload, bool) {
	assert(cfg != nil, "cfg must not be nil")
	assert(cfg.NodeID != "", "node_id must not be empty")
	assert(cfg.AuditTimeoutSec > 0, "audit_timeout_sec must be positive")

	if cfg.AnsiblePlaybookPath == "" {
		return wire.DriftPayload{}, false
	}

	if err := writeAnsibleCfg(); err != nil {
		log.Printf("audit: write ansible.cfg: %v", err)
		return wire.DriftPayload{
			NodeID:        cfg.NodeID,
			TimestampUnix: time.Now().Unix(),
			Status:        statusError,
		}, true
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.AuditTimeoutSec)*time.Second,
	)
	defer cancel()

	// Run as the fiia service user directly — no sudo.
	// NoNewPrivileges=true in the systemd unit hard-blocks sudo, so we suppress
	// become escalation and check as fiia. Baseline files must be readable by fiia
	// (0644 or fiia-owned) for check mode to work.
	// -i localhost, (trailing comma = inline inventory, single host)
	// -c local      (local connection — no SSH, checks the same machine)
	// -e ansible_become=false (playbook has become: true; suppress it — check-only)
	// ansible_local_tmp / ansible_remote_tmp: fiia has no home dir; redirect tmp
	cmd := exec.CommandContext(ctx, "ansible-playbook", "--check", "--diff",
		"-i", "localhost,", "-c", "local",
		"-e", "ansible_become=false",
		"-e", "ansible_remote_tmp=/var/lib/fiia/.ansible/tmp",
		cfg.AnsiblePlaybookPath)
	cmd.Env = append(cmd.Environ(),
		"ANSIBLE_LOCAL_TEMP=/var/lib/fiia/.ansible/tmp",
		"ANSIBLE_CONFIG="+ansibleCfgPath,
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	appendDriftLog(cfg.DriftLogPath, out.Bytes())

	status, tasks := interpretResult(ctx, err, out.Bytes())
	if status == statusTimeout {
		log.Printf("audit: timeout after %.1fs (repeated short timeouts indicate CPUQuota too low)", elapsed.Seconds())
	} else {
		log.Printf("audit: completed in %.1fs status=%s changed=%d", elapsed.Seconds(), status, len(tasks))
	}
	return wire.DriftPayload{
		NodeID:        cfg.NodeID,
		TimestampUnix: time.Now().Unix(),
		Status:        status,
		TasksChanged:  tasks,
	}, true
}

func interpretResult(ctx context.Context, err error, output []byte) (string, []string) {
	if ctx.Err() == context.DeadlineExceeded {
		return statusTimeout, nil
	}

	if err != nil {
		exit_err, ok := err.(*exec.ExitError)
		if !ok {
			log.Printf("audit: exec error: %v", err)
			return statusError, nil
		}
		switch exit_err.ExitCode() {
		case exit_code_changed:
			// Some ansible versions exit 2 for check-mode changes.
			return statusDriftDetected, parseChangedTasks(output)
		case exit_code_oom:
			return statusResourceExceeded, nil
		default:
			return fmt.Sprintf("AUDIT_EXIT_%d", exit_err.ExitCode()), nil
		}
	}

	// ansible exits 0 on success regardless of check-mode changes.
	// Parse PLAY RECAP to detect "changed>0" which means drift was found.
	if tasks := parseChangedTasks(output); len(tasks) > 0 {
		return statusDriftDetected, tasks
	}
	return statusOK, nil
}

func parseChangedTasks(output []byte) []string {
	var tasks []string
	var last_task string
	for line := range strings.SplitSeq(string(output), "\n") {
		if strings.HasPrefix(line, "TASK [") {
			end := strings.Index(line, "]")
			if end > 6 {
				last_task = line[6:end]
			}
		} else if strings.Contains(line, "changed:") && last_task != "" {
			tasks = append(tasks, last_task)
			last_task = ""
		}
	}
	return tasks
}

const (
	ansibleCfgPath = "/var/lib/fiia/.ansible/audit.cfg"
	ansibleCfgBody = "[defaults]\n" +
		"gathering = explicit\n" +
		"local_tmp = /var/lib/fiia/.ansible/tmp\n" +
		"remote_tmp = /var/lib/fiia/.ansible/tmp\n"
)

// writeAnsibleCfg writes a minimal ansible.cfg before each audit invocation.
// This ensures gathering=explicit and correct tmp paths are always in effect,
// regardless of whether the bootstrap-deployed /var/lib/fiia/ansible.cfg is present.
func writeAnsibleCfg() error {
	if err := os.MkdirAll("/var/lib/fiia/.ansible", 0700); err != nil {
		return err
	}
	return os.WriteFile(ansibleCfgPath, []byte(ansibleCfgBody), 0640)
}

func appendDriftLog(path string, data []byte) {
	if path == "" || len(data) == 0 {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		log.Printf("audit: open drift log %q: %v", path, err)
		return
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		log.Printf("audit: write drift log: %v", err)
	}
}

func assert(condition bool, message string) {
	if !condition {
		panic("agent/audit: assertion failed: " + message)
	}
}
