.PHONY: build test test-linux lint \
        dev-init dev-setup dev-vm-create dev-vm-start dev-vm-stop dev-vm-status \
        dev-build dev-certs dev-certs-force \
        dev-inventory dev-hub dev-deploy dev-run dev-logs dev-drift-log \
        dev-journal dev-watch dev-stop \
        dev-drift dev-restore dev-check-drift dev-collection-test

# ── build ──────────────────────────────────────────────────────────────────────

build:
	go build ./...

test:
	go test ./...

test-linux:
	dev/test-linux.sh $(ARGS)

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
DEV_SECRET   := 0000000000000000000000000000000000000000000000000000000000000001

# ── dev setup ──────────────────────────────────────────────────────────────────
# dev-init: one-shot first-time setup (detect backend, create VM, start it)
# After this: open two terminals — run `make dev-hub` in one, `make dev-deploy` in the other.

dev-init:
	bash dev/setup.sh
	$(VM) create
	$(VM) start
	@echo ""
	@echo "VM ready. Next:"
	@echo "  terminal 1: make dev-hub"
	@echo "  terminal 2: make dev-deploy"

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

dev-certs:
	go run ./dev/gen_certs
	@echo "certs in dev/ca/ (no-op if already present; use dev-certs-force to rotate)"

dev-certs-force:
	go run ./dev/gen_certs -- --force
	@echo "certs rotated in dev/ca/ — restart hub and redeploy agents"

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

dev-hub: dev-certs
	go run ./cmd/hub \
	  -config dev/hub.toml \
	  -seed-node "$(DEV_NODE_ID):$(DEV_SECRET)"

# ── dev deploy ─────────────────────────────────────────────────────────────────

dev-deploy: dev-build dev-inventory
	@test -n "$(ANSIBLE_PLAYBOOK)" || \
	  { echo "ERROR: ansible-playbook not found. Fix: brew install ansible  OR  pip3 install ansible"; exit 1; }
	$(ANSIBLE_PLAYBOOK) \
	  -i deploy/ansible/inventory/dev.ini \
	  -e "fiia_agent_binary=$(CURDIR)/$(LINUX_BINARY)" \
	  -e "fiia_node_id=$(DEV_NODE_ID)" \
	  -e "fiia_hub_hmac_secret_hex=$(DEV_SECRET)" \
	  deploy/ansible/dev-bootstrap.yml

# ── dev observe ────────────────────────────────────────────────────────────────

dev-run:
	$(VM) shell sudo systemctl status fiia-agent

dev-logs:
	$(VM) shell sudo tail -f /var/log/fiia/agent.log

dev-drift-log:
	$(VM) shell sudo tail -f /var/log/fiia/drift.log

dev-journal:
	$(VM) shell sudo journalctl -u fiia-agent -f

dev-watch:
	$(VM) shell sudo tail -f /var/log/fiia/agent.log /var/log/fiia/drift.log \
	  | awk '/^==> /{split($$2,a,"/"); src=a[length(a)]; next} {print src": "$$0}'

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

# ── collection module test (Docker) ───────────────────────────────────────────
# Tests fiia.fleet.manifest against a real Ubuntu container via docker exec.
# No VM or SSH needed — works wherever Docker is available.
#
# Requires: docker  +  ansible-galaxy collection install community.docker

DOCKER          := $(shell which docker 2>/dev/null)
DOCKER_CTR      := fiia-collection-test
COLLECTION_PATH := $(CURDIR)/ansible/collections

dev-collection-test:
	@test -n "$(DOCKER)" || \
	  { echo "ERROR: docker not found. Install Docker Desktop, OrbStack, or Colima."; exit 1; }
	@test -n "$(ANSIBLE_PLAYBOOK)" || \
	  { echo "ERROR: ansible-playbook not found. Fix: brew install ansible"; exit 1; }
	@ansible-galaxy collection list 2>/dev/null | grep -q 'community\.docker' || \
	  { echo "Installing community.docker collection..."; ansible-galaxy collection install community.docker; }
	@echo "=== starting Ubuntu container ==="
	@docker rm -f $(DOCKER_CTR) 2>/dev/null || true
	docker run -d --name $(DOCKER_CTR) ubuntu:24.04 sleep infinity
	@echo "=== installing test dependencies ==="
	docker exec $(DOCKER_CTR) bash -c \
	  "apt-get update -q && apt-get install -y -q --no-install-recommends python3 openssh-server"
	@echo "=== running manifest module tests ==="
	ANSIBLE_COLLECTIONS_PATHS=$(COLLECTION_PATH) \
	$(ANSIBLE_PLAYBOOK) \
	  -i "$(DOCKER_CTR)," \
	  -e "ansible_connection=community.docker.docker" \
	  -e "ansible_python_interpreter=/usr/bin/python3" \
	  deploy/ansible/collection-test.yml ; \
	STATUS=$$? ; \
	docker rm -f $(DOCKER_CTR) ; \
	exit $$STATUS
