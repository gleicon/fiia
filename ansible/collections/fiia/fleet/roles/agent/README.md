# fiia.fleet.agent role

Installs fiia-agent (drift checker + OTel emitter), records the drift
manifest as the last step (via the agent binary `-write-manifest`), and
verifies the baseline reads clean.
No hub, no secrets, no certificates.

> **Golden rule — document what you want checked.** Fiia only checks what the
> manifest records. Snapshot mode captures all packages + services
> automatically, but **files are never snapshotted**: a file is tracked only if
> you list it in the `manifest` module (`fiia_manifest_files`) or a
> `-scan-playbook` run derives it from `copy:`/`template:`/`lineinfile:` tasks.
> If it matters, promise it — otherwise changes to it are invisible.

## Install

```sh
ansible-galaxy collection install \
  git+https://github.com/gleicon/fiia.git#/ansible/collections/fiia/fleet
```

Or vendor into your project:

```sh
mkdir -p collections/ansible_collections
cp -r /path/to/fiia/ansible/collections/fiia collections/ansible_collections/
```

```ini
# ansible.cfg
[defaults]
collections_path = ./collections
```

## Usage

```yaml
# site.yml — your existing provisioning playbook
- name: Provision web servers
  hosts: webservers
  become: true
  roles:
    - fiia.fleet.agent   # installs fiia-agent, records + verifies manifest

  vars:
    fiia_agent_binary: dist/fiia-agent-linux-amd64
    fiia_otlp_endpoint: "http://otelcol.internal:4318"  # omit for stdout export
    fiia_manifest_files:
      - /etc/nginx/nginx.conf
      - /etc/ssh/sshd_config
    fiia_manifest_packages:
      - nginx
      - openssh-server
    fiia_manifest_services:
      - nginx
      - ssh

  tasks:
    # ... your existing tasks ...
    - name: Install nginx
      ansible.builtin.apt:
        name: nginx
        state: present

    - name: Deploy nginx config
      ansible.builtin.copy:
        src: nginx.conf
        dest: /etc/nginx/nginx.conf
        mode: "0644"
```

## Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `fiia_agent_binary` | — (required) | Local path to `fiia-agent` binary to deploy |
| `fiia_otlp_endpoint` | unset (stdout) | OTLP/HTTP receiver for verdicts + liveness |
| `fiia_manifest_path` | `/etc/fiia/manifest.json` | Where the manifest is recorded |
| `fiia_manage_manifest` | `true` | Record + verify the manifest in the role (disable when your own tasks do it) |
| `fiia_manifest_snapshot` | `false` | Also record full package/service lists; later additions flag `pkg:unauthorized:*` / `svc:unauthorized:*` |
| `fiia_manifest_files` | `[]` | Absolute file paths to track |
| `fiia_manifest_packages` | `[]` | Package names to track |
| `fiia_manifest_services` | `[]` | Service names to track |
| `fiia_heartbeat_interval_sec` | `60` | Liveness emission interval (`fiia.alive`) |
| `fiia_audit_interval_sec` | `1200` | Manifest check interval |
| `fiia_audit_jitter_max_sec` | `120` | Splay jitter cap |

## Alerting (your OTel backend)

| Signal | Meaning | Suggested alert |
|--------|---------|-----------------|
| absent `fiia.alive{host.name}` | Node silent | dead-man / missing-series alert |
| `fiia.drift.status == 1` | Drift detected (deviations in the log record) | page / ticket |
| `fiia.drift.status == 2` | Check error (manifest missing) | provisioning follow-up |
