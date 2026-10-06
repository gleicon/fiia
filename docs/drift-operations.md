# Fiia — drift operations: find it, fix it, verify it

How an operator finds out *what* drifted, fixes it, and keeps the manifest as
the source of truth — for one node or a fleet of hundreds.

**Default posture: detect and alert. Nothing is auto-fixed.** The standard fix
path is Infrastructure as Code: re-run your provisioning playbook. Remediation
is an explicit opt-in (`-remediate` / `remediate = true`) — never the default.

> ## 🥇 THE GOLDEN RULE — everything you want checked must be documented
>
> **Fiia only checks what is written in the manifest, and the manifest is a
> curated promise — not an audit log and not a full OS snapshot.**
>
> - **Packages & services**: snapshot mode records *all* installed packages and
>   active services, so anything extra is caught automatically
>   (`pkg:unauthorized`, `svc:unauthorized`). No manual work needed.
> - **Files**: there is **no file snapshot**. A file is checked **only if** you
>   document it — either:
>   1. **in Ansible** — declare it in a `copy:`/`template:`/`lineinfile:` task
>      and let `-scan-playbook` derive it (static destinations only; loops and
>      templated `dest:` are skipped with warnings), **or**
>   2. **directly in the manifest** — list the path with
>      `-write-manifest -files …` (the `fiia.fleet.agent` role's
>      `fiia_manifest_files` does this for you).
>
>   Anything you don't document (e.g. `/etc/resolv.conf` in a play that never
>   declares it) is **invisible to fiia**, no matter how Ansible or the OS
>   changes it.
>
> **Rule of thumb:** *if it matters, promise it.* Every file your service
> depends on should appear in your Ansible play (and thus in the manifest), or
> be listed explicitly in the manifest. Review `-scan-playbook` warnings — they
> are the files Ansible touches that fiia *cannot* see yet.

## 1. Finding what drifted (fleet-wide, no logging into servers)

The drift **detail lives in the `fiia.drift.details` metric**, which the agent
emits only while drifting, with the deviation list as the `deviations` label:

| Where | What you see |
|---|---|
| **Grafana** dashboard *"Fiia — drift detection"* | **"Drifted nodes (host → deviations)"** table — every drifted host + its deviations; **"Firing alerts"** table shows the alert with the `deviations` column |
| **Grafana/Prometheus alert** | "Configuration drift detected on web-01" — the description points to the *Drifted nodes* panel / `fiia_drift_details` for the exact deviations |
| **Prometheus** (http://localhost:9090) | `fiia_drift_details` — per-node detail; `fiia_drift_status` — 0/1/2; `ALERTS` — firing |

Queries for a fleet:

```promql
# every drifted node and what's wrong with it
fiia_drift_details
# which nodes have vim drift, without logging in
fiia_drift_details{deviations=~".*vim.*"}
# count of drifted nodes
count(fiia_drift_details)
```

Per-node, from the shell (root or the service user):

```sh
fiia-agent -check -manifest /etc/fiia/manifest.json            # text
fiia-agent -check -format json -manifest /etc/fiia/manifest.json # machine-readable
```

## 2. Deviation legend

| Deviation | Meaning |
|---|---|
| `file:hash_mismatch:<path>` | file content changed |
| `file:missing:<path>` | **tracked file was deleted** |
| `file:mode_mismatch:<path>` | permissions differ |
| `file:is_directory:<path>` | tracked file is now a directory |
| `pkg:missing:<name>` | declared package not installed |
| `pkg:version_mismatch:<name>:<have>:<want>` | wrong version |
| `pkg:unauthorized:<name>` | installed but not in the snapshot (promise violation) |
| `svc:inactive:<name>` / `svc:disabled:<name>` | declared service stopped / not enabled |
| `svc:unauthorized:<name>` | running service not in the snapshot |
| `svc:unverifiable:<name>…` | systemd can't be queried (D-Bus denied) — grant access or run as root |

## 3. Fixing (in order of preference)

**A. IaS — re-run the provisioning playbook (restores files, services, and the manifest).**

```sh
ansible-playbook -i deploy/ansible/inventory/dev.ini \
  -e fiia_agent_binary=./fiia-agent-linux-amd64 \
  deploy/ansible/dev-bootstrap.yml
```
Your real play re-applies `template:`/`copy:`/`lineinfile:` content (so deleted
files come back), service states, and re-records the manifest as the new
baseline. **Be careful:** re-recording *adopts* the current state — if you
installed vim and just re-run, vim becomes "authorized". Only re-run after you
decide the current state is correct.

**B. Manual (single node).**

```sh
# package you added — remove it, or keep it and adopt it via IaS
apt-get purge -y vim && apt-get autoremove -y --purge        # dnf remove -y vim

# deleted tracked file — restore from the playbook's source
ansible-playbook ...   # or copy the file back from VCS

# service state — re-match the manifest
systemctl start --now nginx && systemctl enable nginx        # or stop/disable
```

**C. Authorized auto-remediation (explicit opt-in, off by default).**

> ## ⚠️ BIG WARNING — remediation is NOT a full config-management fix
>
> `-remediate` / `remediate = true` enforces **only the packages & services
> recorded in the manifest snapshot**. The manifest is *not* a complete
> system-state model. Fiia does **not** know about, and will **not** touch:
>
> - users / groups, sudoers, SSH keys and authorized keys
> - cron jobs, systemd timers, drop-in overrides
> - firewall / iptables / nftables rules, network config
> - sysctl / kernel parameters, security policies (SELinux/AppArmor contexts)
> - environment vars, capabilities, custom Ansible module state
>
> Remediating and assuming "everything is fixed" because drift is gone is
> **wrong and dangerous** — packages are only a slice of what a config
> management system like Ansible manages. **Re-running your provisioning
> playbook is the only complete remediation.** Treat `-remediate` as a quick
> package/service tidy-up, never as a substitute for IaS.

One-shot:

```sh
# removes packages/services not in the snapshot, fixes declared service states
# (file deviations are still reported — only the playbook can restore content)
fiia-agent -check -remediate -manifest /etc/fiia/manifest.json
```

Daemon (CFEngine-style promise enforcement on every audit):

```toml
[agent]
remediate = true   # OFF by default; deliberate opt-in
```

## 4. Manifest as the immutable source of truth (promises)

The design follows **promises theory**:

- **The manifest is the promise snapshot** — it records what the node should
  look like (`files`, `packages`, `services`, plus the full `package_snapshot`
  / `service_snapshot` in snapshot mode). The agent does not alter it.
- **The provisioning playbook is the source of truth for content** — file
  bytes come from `template:`/`copy:` sources (IaS), not from the manifest.
  That's why the agent remediates packages/services but **never** rewrites
  files: it can't invent content, and IaS must stay the single source of truth.
- **The agent is the promise enforcer** — it *detects* violations by default
  and, only when explicitly authorized, *enforces* packages/services back to
  the snapshot.

Rules to keep promises meaningful:

1. Never hand-edit the manifest. Change files/services via your playbooks, then
   let provisioning re-record it.
2. Keep it fresh. A stale manifest is a stale promise; remediation enforces
   whatever is in the snapshot. If a package is intentionally added, re-run the
   play to re-baseline *before* it looks like unauthorized drift.
3. Audit remediation: the agent logs every action it takes
   (`remediate: removed 2 package(s): vim, vim-common`), so enforcement is
   visible and reversible.

> ## ⚠️ Promises scope limit
>
> A promise is only as good as what the manifest records: **files, packages,
> services** (+ snapshots of installed packages / active services). Ansible
> (and any real config-management system) manages far more — users, cron,
> firewall, sysctl, SELinux, capabilities, custom modules. Those are **out of
> the manifest's scope**, so Fiia neither detects nor remediates them. If you
> rely on auto-remediation, you are enforcing *only the recorded promise*;
> the rest of the system still depends on your provisioning playbook being
> re-run. Always pair detection/remediation with IaS, and never let a stale or
> partial snapshot become the excuse to stop running Ansible.

Example promise flow (what you just did):

```sh
# operator installed vim on a node by hand
# 1. alert fires + Grafana "Drifted nodes" shows: pkg:unauthorized:vim (+ deps)
# 2. decide: vim was a mistake
fiia-agent -check -remediate -manifest /etc/fiia/manifest.json
#    remediate: removed 2 package(s): vim, vim-common
# 3. verify
fiia-agent -check -manifest /etc/fiia/manifest.json      # OK: no drift
# alert auto-resolves, status 0, history kept
```

The same flow, fleet-wide: authorize `remediate = true` on nodes (or run
`-remediate` via `ansible -m command`) and the promise is enforced everywhere;
Grafana/Prometheus show every node's deviations until they're back in
compliance.