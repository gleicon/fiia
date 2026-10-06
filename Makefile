.PHONY: build test test-linux lint e2e e2e-ansible e2e-systemd e2e-live e2e-live-down e2e-live-webhook \
        dev-init dev-setup dev-vm-create dev-vm-start dev-vm-stop dev-vm-status \
        dev-build \
        dev-inventory dev-deploy dev-run dev-logs \
        dev-journal dev-watch dev-stop \
        dev-drift dev-restore dev-check-drift

# ── build ──────────────────────────────────────────────────────────────────────

build:
	go build ./...

test:
	go test ./...

test-linux:
	dev/test-linux.sh $(ARGS)

e2e:
	bash e2e/run.sh

e2e-ansible:
	bash e2e/ansible/run.sh

e2e-systemd:
	bash e2e/systemd/run.sh

# Live observability run: daemon -> collector -> Prometheus -> Grafana.
# Keeps the stack running for real-time viewing. Tear down with e2e-live-down.
e2e-live:
	bash e2e/observe/run.sh

e2e-live-down:
	bash e2e/observe/run.sh down

# Optional local webhook listener: prints Grafana alert notifications as they
# fire during e2e-live. Ctrl+C to stop.
e2e-live-webhook:
	bash e2e/observe/run-webhook.sh

# ── VM backend ─────────────────────────────────────────────────────────────────
# Backend is detected once by running: make dev-setup
# Result is stored in dev/.backend and loaded here.
# Override for a single target: make dev-deploy DEV_VM_BACKEND=colima
#
# Supported backends: lima | colima | apple-container | multipass | local
# See dev/setup.sh for install instructions.

-include dev/.backend

VM := dev/vm.sh

# On Linux, always local even without running dev-setup.
ifeq ($(shell uname -s),Linux)
  DEV_VM_BACKEND ?= local
endif

# Arch of the target VM for cross-compilation.
VM_ARCH      := $(shell $(VM) arch 2>/dev/null)
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

# ── ansible ────────────────────────────────────────────────────────────────────
ANSIBLE_BIN      := $(shell which ansible-playbook 2>/dev/null || \
                             ls /opt/homebrew/bin/ansible-playbook 2>/dev/null || \
                             ls /usr/local/bin/ansible-playbook 2>/dev/null)
ANSIBLE_DIRECT   := $(shell test -n "$(ANSIBLE_BIN)" && "$(ANSIBLE_BIN)" --version >/dev/null 2>&1 && echo yes)
ANSIBLE_PYTHON   := $(shell \
  for py in python3 python3.13 python3.12 python3.11 python3.10 python3.9; do \
    if command -v $$py >/dev/null 2>&1 && $$py -c "import ansible" 2>/dev/null; then \
      echo $$py; break; \
    fi; \
  done)
ANSIBLE_PLAYBOOK ?= $(if $(ANSIBLE_DIRECT),$(ANSIBLE_BIN),\
                    $(if $(and $(ANSIBLE_BIN),$(ANSIBLE_PYTHON)),$(ANSIBLE_PYTHON) $(ANSIBLE_BIN),))

LINUX_BINARY := fiia-agent-linux-$(LINUX_GOARCH)
DEV_NODE_ID  := $(shell $(VM) node-id 2>/dev/null || echo dev-node)

# ── dev setup ──────────────────────────────────────────────────────────────────
# dev-init: one-shot first-time setup (detect backend, create VM, start it)
# After this: run `make dev-deploy` to provision the agent on the target.

dev-init:
	bash dev/setup.sh
	$(VM) create
	$(VM) start
	@echo ""
	@echo "VM ready. Next: make dev-deploy"

dev-setup:
	bash dev/setup.sh

# ── dev VM lifecycle ───────────────────────────────────────────────────────────

dev-vm-create:
	$(VM) create

dev-vm-start:
	$(VM) start

dev-vm-stop:
	$(VM) stop

dev-vm-status:
	$(VM) status

# ── dev build & certs ──────────────────────────────────────────────────────────

dev-build:
	GOOS=linux GOARCH=$(LINUX_GOARCH) CGO_ENABLED=0 \
	  go build -o $(LINUX_BINARY) ./cmd/agent
	@echo "built $(LINUX_BINARY)"

# ── dev inventory ──────────────────────────────────────────────────────────────

dev-inventory:
	$(VM) ssh-config > /tmp/fiia-ssh.cfg
	@mkdir -p deploy/ansible/inventory
	@case "$(DEV_VM_BACKEND)" in \
	  lima) \
	    printf '[fleet]\n$(DEV_VM_NAME) ansible_ssh_common_args="-F /tmp/fiia-ssh.cfg"\n' \
	      > deploy/ansible/inventory/dev.ini ;; \
	  colima) \
	    printf '[fleet]\ncolima-vm ansible_host=colima ansible_ssh_common_args="-F /tmp/fiia-ssh.cfg"\n' \
	      > deploy/ansible/inventory/dev.ini ;; \
	  multipass) \
	    printf '[fleet]\n$(DEV_VM_NAME) ansible_ssh_common_args="-F /tmp/fiia-ssh.cfg"\n' \
	      > deploy/ansible/inventory/dev.ini ;; \
	  local) \
	    printf '[fleet]\nlocalhost ansible_connection=local\n' \
	      > deploy/ansible/inventory/dev.ini ;; \
	  *) \
	    printf '[fleet]\n$(DEV_VM_NAME) ansible_ssh_common_args="-F /tmp/fiia-ssh.cfg"\n' \
	      > deploy/ansible/inventory/dev.ini ;; \
	esac
	@echo "inventory written (backend: $(DEV_VM_BACKEND))"

# ── dev hub ────────────────────────────────────────────────────────────────────

# ── dev deploy ─────────────────────────────────────────────────────────────────

dev-deploy: dev-build dev-inventory
	@test -n "$(ANSIBLE_PLAYBOOK)" || \
	  { echo "ERROR: ansible-playbook not found. Fix: brew install ansible  OR  pip3 install ansible"; exit 1; }
	ANSIBLE_COLLECTIONS_PATHS=$(CURDIR)/ansible/collections \
	$(ANSIBLE_PLAYBOOK) \
	  -i deploy/ansible/inventory/dev.ini \
	  -e "fiia_agent_binary=$(CURDIR)/$(LINUX_BINARY)" \
	  -e "fiia_node_id=$(DEV_NODE_ID)" \
	  deploy/ansible/dev-bootstrap.yml

# ── dev observe ────────────────────────────────────────────────────────────────

dev-run:
	$(VM) shell sudo systemctl status fiia-agent

dev-logs:
	$(VM) shell sudo tail -f /var/log/fiia/agent.log

dev-journal:
	$(VM) shell sudo journalctl -u fiia-agent -f

dev-watch:
	$(VM) shell sudo journalctl -u fiia-agent -f

dev-stop:
	$(VM) shell sudo systemctl stop fiia-agent

# ── drift detection test ───────────────────────────────────────────────────────

dev-drift:
	@echo "=== introducing drift ==="
	$(VM) shell sudo sh -c \
	  'printf "# UNAUTHORIZED EDIT\nversion: 99\nstate: compromised\n" > /etc/fiia/sentinel'
	$(VM) shell sudo sh -c \
	  'echo "banner changed by attacker" > /etc/motd'
	@echo ""
	@echo "Drift introduced. Agent detects within audit_interval_sec (~120s)."

dev-restore:
	@echo "=== restoring baseline ==="
	$(VM) shell sudo sh -c \
	  'printf "# managed by fiia baseline \342\200\224 do not edit\nversion: 1\nstate: ok\n" > /etc/fiia/sentinel'
	$(VM) shell sudo sh -c \
	  'echo "Authorized access only. Activity is monitored by Fiia." > /etc/motd'
	@echo "Baseline restored."

dev-check-drift:
	@echo "=== on-node verdict (exit 0 clean, 1 drift) ==="
	$(VM) shell sudo /usr/local/bin/fiia-agent -check -manifest /etc/fiia/manifest.json; true
	@echo ""
	@echo "=== recent agent log (OTel export target) ==="
	$(VM) shell sudo tail -n 20 /var/log/fiia/agent.log
