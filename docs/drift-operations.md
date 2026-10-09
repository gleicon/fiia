# Fiia drift operations

Find out what drifted, fix it, and verify the fix, for one node or a fleet.

Default posture: detect and alert. Nothing is auto-fixed. The standard fix path
is Infrastructure as Code: re-run your provisioning playbook. Remediation is an
explicit opt-in (`-remediate` / `remediate = true`), never the default.

## Golden rule

Fiia checks only what the manifest documents. Packages and services are
snapshotted automatically (anything extra flags as `pkg:unauthorized` /
`svc:unauthorized`). Files are never snapshotted: document each file you want
checked in Ansible (a `copy:`/`template:`/`lineinfile:` task, derived by
`-scan-playbook`) or directly in the manifest (`-write-manifest -files ...`,
or the role's `fiia_manifest_files`). If it matters, promise it.

## Find what drifted

The detail lives in the `fiia.drift.details` metric, which the agent emits only
while drifting, with the deviation list as the `deviations` label.

| Where | What you see |
|-------|--------------|
| Grafana dashboard *Fiia, drift detection* | *Drifted nodes (host, deviations)* table, per drifted node |
| Prometheus query | `fiia_drift_details` and `fiia_drift_status`, `ALERTS` |

Fleet queries:

```promql
# every drifted node and what is wrong with it
fiia_drift_details
# nodes with vim drift, without logging in
fiia_drift_details{deviations=~".*vim.*"}
# count of drifted nodes
count(fiia_drift_details)
```

Per node, from the shell:

```sh
fiia-agent -check -manifest /etc/fiia/manifest.json
fiia-agent -check -format json -manifest /etc/fiia/manifest.json
```

## Deviation legend

| Deviation | Meaning |
|-----------|---------|
| `file:hash_mismatch:<path>` | file content changed |
| `file:missing:<path>` | tracked file was deleted |
| `file:mode_mismatch:<path>` | permissions differ |
| `file:is_directory:<path>` | tracked file is now a directory |
| `file:size_mismatch:<path>` | size differs (check "size") |
| `file:mtime_mismatch:<path>` | modification time differs (check "mtime") |
| `pkg:missing:<name>` | declared package not installed |
| `pkg:version_mismatch:<name>:<have>:<want>` | wrong version |
| `pkg:unauthorized:<name>` | installed but not in the snapshot |
| `svc:inactive:<name>` / `svc:disabled:<name>` | declared service stopped / not enabled |
| `svc:unauthorized:<name>` | running service not in the snapshot |
| `svc:unverifiable:<name>` | systemd cannot be queried (D-Bus denied); grant access or run as root |

## Fix

Re-run the provisioning playbook. It re-applies `template:`/`copy:`/`lineinfile:`
content (so deleted files come back), service states, and re-records the
manifest as the new baseline. Be careful: re-recording adopts the current
state. If you installed vim and just re-run, vim becomes authorized. Re-run
only after you decide the current state is correct.

Manual, single node:

```sh
# package you added: remove it, or keep it and adopt it via IaS
apt-get purge -y vim && apt-get autoremove -y --purge        # dnf remove -y vim

# deleted tracked file: restore from the playbook's source
ansible-playbook ...   # or copy the file back from VCS

# service state: re-match the manifest
systemctl start --now nginx && systemctl enable nginx        # or stop/disable
```

### Authorized auto-remediation (opt-in, off by default)

> Warning: `-remediate` / `remediate = true` enforces only the packages and
> services recorded in the manifest snapshot. The manifest is not a complete
> system-state model. Fiia does not know about and will not touch users,
> sudoers, SSH keys, cron jobs, firewall rules, sysctl parameters, or any other
> state Ansible manages. Re-running the provisioning playbook is the only
> complete remediation. Treat `-remediate` as a package/service tidy-up, never
> as a substitute for IaS.

```sh
# removes packages/services not in the snapshot, fixes declared service states.
# File deviations are still reported; only the playbook can restore content.
fiia-agent -check -remediate -manifest /etc/fiia/manifest.json
```

Daemon-mode enforcement on every audit:

```toml
[agent]
remediate = true   # off by default; deliberate opt-in
```

## Promises

- The manifest is the promise snapshot. It records files, packages, services,
  and (in snapshot mode) the full package and service lists. The agent does not
  alter it.
- The provisioning playbook is the source of truth for file content. Fiia
  remediates packages and services but never rewrites files, because it has no
  content to write and IaS is where file content lives.
- Keep promises fresh. A stale manifest is a stale promise; re-baseline with a
  play run after any intentional change. The agent logs every remediation
  action, so enforcement is visible and reversible.

Example promise flow:

```sh
# operator installed vim on a node by hand
# 1. alert fires; Grafana *Drifted nodes* shows pkg:unauthorized:vim (+ deps)
# 2. decide: vim was a mistake
fiia-agent -check -remediate -manifest /etc/fiia/manifest.json
#    remediate: removed 2 package(s): vim, vim-common
# 3. verify
fiia-agent -check -manifest /etc/fiia/manifest.json      # OK: no drift
# alert auto-resolves, status 0, history kept
```

Fleet-wide, run `-remediate` via `ansible -m command` or set `remediate = true`
on the nodes. Grafana and Prometheus show every node's deviations until it is
back in compliance.