# fiia.fleet.agent role + fiia.fleet.manifest module

## Install

```sh
ansible-galaxy collection install \
  git+https://gitlab.internal/infra/fiia.git#ansible/collections/fiia/fleet,v0.1.0
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
    - fiia.fleet.agent   # installs fiia-agent

  vars:
    fiia_hub_addr: hub.internal:9443
    fiia_hub_api_addr: hub.internal:9091
    fiia_hmac_secret_hex: "{{ vault_fiia_hmac_secret_hex }}"
    fiia_ca_cert: "{{ lookup('file', 'files/root_ca.pem') }}"
    fiia_agent_binary: dist/fiia-agent-linux-amd64

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

    # Last task: snapshot desired state for fiia drift monitoring
    - name: Update fiia drift manifest
      fiia.fleet.manifest:
        files:
          - /etc/nginx/nginx.conf
          - /etc/ssh/sshd_config
        packages:
          - nginx
          - openssh-server
        services:
          - nginx
          - { name: ssh, running: true, enabled: true }
```

## Required variables

| Variable | Description |
|----------|-------------|
| `fiia_hub_addr` | Hub TLS ingest address (`host:port`) |
| `fiia_hub_api_addr` | Hub REST API address for post-deploy handshake |
| `fiia_hmac_secret_hex` | Per-node 32-byte HMAC secret (hex) |
| `fiia_ca_cert` | PEM content of root CA certificate |
| `fiia_agent_binary` | Local path to `fiia-agent` binary to deploy |

## Drift check mode

By default the agent uses manifest-based drift check (`fiia_manifest_path`).
To use the legacy ansible `--check` path instead, set:

```yaml
fiia_manifest_path: ""
fiia_ansible_playbook_path: "/etc/fiia/baseline.yml"
```
