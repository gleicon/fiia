.PHONY: build test lint \
        dev-vm-start dev-vm-stop dev-build dev-certs dev-certs-force \
        dev-inventory dev-hub dev-deploy dev-run dev-logs dev-drift-log dev-journal dev-watch dev-stop \
        dev-drift dev-restore dev-check-drift

# ── build ──────────────────────────────────────────────────────────────────────

build:
	go build ./...

test:
	go test ./...

# ── arch detection ─────────────────────────────────────────────────────────────
# Query the running Colima VM for its arch. Falls back to host arch if VM is down.

VM_ARCH      := $(shell colima ssh -- uname -m 2>/dev/null)
ifeq ($(VM_ARCH),aarch64)
LINUX_GOARCH := arm64
else ifeq ($(VM_ARCH),arm64)
LINUX_GOARCH := arm64
else ifneq ($(VM_ARCH),)
LINUX_GOARCH := amd64
else
HOST_ARCH    := $(shell uname -m)
ifeq ($(HOST_ARCH),arm64)
LINUX_GOARCH := arm64
else
LINUX_GOARCH := amd64
endif
endif

# Locate a working ansible-playbook invocation. Priority:
#   1. Direct invocation works (brew install ansible — correct shebang): use it
#   2. Find a Python with ansible importable and prefix the script (pip install)
#   3. Neither → empty → dev-deploy fails with a clear message
# Install on macOS: brew install ansible
ANSIBLE_BIN     := $(shell which ansible-playbook 2>/dev/null || \
                           ls /opt/homebrew/bin/ansible-playbook 2>/dev/null || \
                           ls /usr/local/bin/ansible-playbook 2>/dev/null)
# Test using the full path so Make's restricted PATH doesn't hide brew binaries.
ANSIBLE_DIRECT  := $(shell test -n "$(ANSIBLE_BIN)" && "$(ANSIBLE_BIN)" --version >/dev/null 2>&1 && echo yes)
ANSIBLE_PYTHON  := $(shell \
  for py in python3 python3.13 python3.12 python3.11 python3.10 python3.9; do \
    if command -v $$py >/dev/null 2>&1 && $$py -c "import ansible" 2>/dev/null; then \
      echo $$py; break; \
    fi; \
  done)
ANSIBLE_PLAYBOOK ?= $(if $(ANSIBLE_DIRECT),$(ANSIBLE_BIN),\
                    $(if $(and $(ANSIBLE_BIN),$(ANSIBLE_PYTHON)),$(ANSIBLE_PYTHON) $(ANSIBLE_BIN),))

LINUX_BINARY := fiia-agent-linux-$(LINUX_GOARCH)
DEV_NODE_ID  := colima-dev
DEV_SECRET   := 0000000000000000000000000000000000000000000000000000000000000001

# ── dev VM ─────────────────────────────────────────────────────────────────────

dev-vm-start:
	colima start --cpu 2 --memory 2 --disk 10

dev-vm-stop:
	colima stop

# ── dev build & certs ──────────────────────────────────────────────────────────

dev-build:
	GOOS=linux GOARCH=$(LINUX_GOARCH) CGO_ENABLED=0 \
	  go build -o $(LINUX_BINARY) ./cmd/agent
	@echo "built $(LINUX_BINARY)"

dev-certs:
	go run ./dev/gen_certs
	@echo "certs in dev/ca/ (no-op if already present; use dev-certs-force to rotate)"

dev-certs-force:
	go run ./dev/gen_certs -- --force
	@echo "certs rotated in dev/ca/ — restart hub and redeploy agents"

# ── dev inventory (regenerate on port change) ──────────────────────────────────

dev-inventory:
	colima ssh-config > /tmp/colima-ssh.cfg
	@mkdir -p deploy/ansible/inventory
	@printf '[fleet]\ncolima-vm ansible_host=colima ansible_ssh_common_args="-F /tmp/colima-ssh.cfg"\n' \
	  > deploy/ansible/inventory/dev.ini
	@echo "inventory written to deploy/ansible/inventory/dev.ini"

# ── dev hub (macOS) ────────────────────────────────────────────────────────────
# Seeds the dev node secret then starts the hub.
# Hub listens on all interfaces so the Colima VM can reach it via host.lima.internal.

dev-hub: dev-certs
	go run ./cmd/hub \
	  -config dev/hub.toml \
	  -seed-node "$(DEV_NODE_ID):$(DEV_SECRET)"

# ── dev deploy (agent into Colima VM) ─────────────────────────────────────────

dev-deploy: dev-build dev-inventory
	@test -n "$(ANSIBLE_PLAYBOOK)" || \
	  { echo "ERROR: ansible-playbook not found or ansible not importable."; \
	    echo "Fix: pip3 install ansible  OR  brew install ansible"; exit 1; }
	$(ANSIBLE_PLAYBOOK) \
	  -i deploy/ansible/inventory/dev.ini \
	  -e "fiia_agent_binary=$(CURDIR)/$(LINUX_BINARY)" \
	  -e "fiia_node_id=$(DEV_NODE_ID)" \
	  -e "fiia_hub_hmac_secret_hex=$(DEV_SECRET)" \
	  deploy/ansible/dev-bootstrap.yml

# ── dev observe ────────────────────────────────────────────────────────────────

dev-run:
	colima ssh -- sudo systemctl status fiia-agent

dev-logs:
	colima ssh -- sudo tail -f /var/log/fiia/agent.log

dev-drift-log:
	colima ssh -- sudo tail -f /var/log/fiia/drift.log

dev-journal:
	colima ssh -- sudo journalctl -u fiia-agent -f

dev-watch:
	colima ssh -- sudo tail -f /var/log/fiia/agent.log /var/log/fiia/drift.log \
	  | awk '/^==> /{split($$2,a,"/"); src=a[length(a)]; next} {print src": "$$0}'

dev-stop:
	colima ssh -- sudo systemctl stop fiia-agent

# ── drift detection test ───────────────────────────────────────────────────────
# Simulates an unauthorized change to a baseline-managed file.
# The agent will detect this on its next audit cycle (dev: ~120s).

dev-drift:
	@echo "=== introducing drift ==="
	colima ssh -- sudo sh -c \
	  'printf "# UNAUTHORIZED EDIT\nversion: 99\nstate: compromised\n" > /etc/fiia/sentinel'
	colima ssh -- sudo sh -c \
	  'echo "banner changed by attacker" > /etc/motd'
	@echo ""
	@echo "Drift introduced. Agent detects within audit_interval_sec (~120s)."
	@echo "Watch logs:       make dev-logs"
	@echo "Check drift API:  make dev-check-drift"

dev-restore:
	@echo "=== restoring baseline state ==="
	colima ssh -- sudo sh -c \
	  'printf "# managed by fiia baseline \342\200\224 do not edit\nversion: 1\nstate: ok\n" > /etc/fiia/sentinel'
	colima ssh -- sudo sh -c \
	  'echo "Authorized access only. Activity is monitored by Fiia." > /etc/motd'
	@echo "Baseline restored."

dev-check-drift:
	@echo "=== node status ==="
	@curl -sf http://localhost:9091/nodes/$(DEV_NODE_ID)/status \
	  | python3 -m json.tool 2>/dev/null || echo "(not reachable — is hub running?)"
	@echo ""
	@echo "=== drift events (last 10) ==="
	@curl -sf http://localhost:9091/nodes/$(DEV_NODE_ID)/drift \
	  | python3 -m json.tool 2>/dev/null || echo "(no events yet)"
	@echo ""
	@echo "=== alerts ==="
	@curl -sf http://localhost:9091/alerts \
	  | python3 -m json.tool 2>/dev/null || echo "(no alerts)"
