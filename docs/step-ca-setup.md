# step-ca Setup for Fiia

Fiia uses a private CA (managed by `step-ca`) to issue the hub's TLS certificate. Agents embed the root CA PEM; no public CA is involved.

## Prerequisites

```
brew install step
brew install step-ca          # or: https://smallstep.com/docs/step-ca/installation
```

## 1. Initialise the CA

Run once per environment (staging, production). Store the CA directory on the hub host.

```sh
step ca init \
  --name "Fiia Fleet CA" \
  --dns "hub.example.com" \
  --address ":9443" \
  --provisioner "fiia-admin"
```

This creates `~/.step/` (or `$STEPPATH`). Back up the root key securely.

## 2. Start step-ca

```sh
step-ca $(step path)/config/ca.json
```

Run under systemd in production. The CA need not be co-located with the hub — it only issues certs; the hub serves TLS itself.

## 3. Issue the hub leaf certificate

```sh
step ca certificate hub.example.com hub_cert.pem hub_key.pem \
  --san hub.example.com \
  --not-after 8760h    # 1 year; automate renewal via step-ca ACME or cron
```

Copy `hub_cert.pem` and `hub_key.pem` to the hub host paths referenced in `hub.toml` (`cert_path`, `key_path`).

## 4. Export the root CA for agents

```sh
step ca root root_ca.pem
```

`root_ca.pem` is deployed to `/etc/fiia/root_ca.pem` on every agent node by the Ansible bootstrap playbook (`fiia_ca_cert` inventory variable).

## 5. Renewal

Leaf cert expiry is tracked by `step ca renew`. Add a cron or systemd timer on the hub host:

```sh
# /etc/cron.d/fiia-hub-cert-renew
0 3 * * * root step ca renew --force /etc/fiia/hub_cert.pem /etc/fiia/hub_key.pem && systemctl reload fiia-hub
```

Root CA rotation requires re-deploying `root_ca.pem` to all agent nodes and restarting agents. Plan for this annually.

## 6. Dev CA

For local development, skip step-ca entirely:

```sh
go run ./dev/gen_certs
```

Writes self-signed certs to `dev/ca/`. The dev configs in `dev/agent.toml` and `dev/hub.toml` reference these paths.
