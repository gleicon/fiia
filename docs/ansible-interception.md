# Observing Ansible runs for existing fleets

How a company with large, mature Ansible codebases (Cassandra, Kafka, nginx,
databases) can adopt fiia without breaking their cookbooks down, editing them,
or hand-listing what to track.

## Problem

Existing recipes use templates, variables, loops, includes, imported roles, and
`command`/`shell` tasks. Hand enumeration of every touched file is impractical.
A recipe parser is fragile: it must model the whole language and still misses
things.

## Principle

Observe the effect of the run, not the recipe. Snapshot the node before the
cookbook runs, snapshot it again after, diff to find what changed, and make
that the **ansible inventory** for the node. The ansible inventory is the set
of configuration files Ansible manages there, recorded so fiia can check it.
This term is about the configuration fileset, not Ansible's host inventory
(the list of nodes a play targets). Whatever Ansible did is captured exactly,
including files written by `command`/`shell` and templates resolved at run
time. It does not matter how a file was changed, only whether the run changed
it.

## Mechanism

One wrapper play imports the existing cookbooks with `import_playbook`. Their
recipes are never edited.

```yaml
# site.yml, one new file. Imports the existing cookbooks, untouched.
- name: Fiia record begin
  hosts: all
  tasks:
    - ansible.builtin.command:
        argv: [fiia-agent, -record-begin, -data, /var/lib/fiia/pre.json,
               -dirs, /etc,/usr/local/etc,/opt,/srv]

- import_playbook: plays/cassandra.yml
- import_playbook: plays/kafka.yml
- import_playbook: plays/nginx.yml

- name: Fiia record end
  hosts: all
  post_tasks:
    - ansible.builtin.command:
        argv: [fiia-agent, -record-end, -manifest, /etc/fiia/manifest.json,
               -data, /var/lib/fiia/pre.json,
               -dirs, /etc,/usr/local/etc,/opt,/srv]
```

`record-begin` hashes all regular files under the chosen roots into a pre-index.
`record-end` re-hashes, diffs, adds the changed and new paths to the manifest
file list with their attributes, removes files the run deleted from tracking,
records the installed package and active service snapshots, and writes the
manifest.

On a fresh node one run captures the cookbook footprint. On existing nodes the
manifest accumulates the union of deltas across runs, so the tracked surface
grows as the recipes evolve. Re-running an unchanged recipe produces no diff
and does not grow the manifest.

The fiia agent CLI uses flags, not subcommands:

```sh
fiia-agent -record-begin -data /var/lib/fiia/pre.json \
  -dirs /etc,/usr/local/etc,/opt,/srv
# ... existing cookbooks run ...
fiia-agent -record-end -manifest /etc/fiia/manifest.json \
  -data /var/lib/fiia/pre.json -dirs /etc,/usr/local/etc,/opt,/srv
```

The index file and the manifest are written owner-only. The commands reuse the
agent's hashing and package/service snapshot code.

## What is recorded per file

| Attribute | Source | Checkable |
|-----------|--------|-----------|
| sha256 | hash at record time | yes, default |
| mode | stat | yes, default |
| size | stat | optional |
| mtime | stat | optional |

Packages and services need no enumeration: snapshot mode records every
installed package and active service, and the manifest records it.

## Check fidelity

The check compares sha256 and mode by default. Compare size and mtime too when
the operator sets the check set in `agent.toml`, for example
`check = ["sha256", "mode", "mtime"]`. mtime catches files rewritten with
identical content (cron, sed, logrotate-style tools). Size catches truncation.
The attributes are stored at record time, so the fidelity decision can be made
later without re-provisioning.

## Design review

Decisions that keep this small:

- Observe the effect, not the recipe. No YAML parser, no Ansible module
  semantics. The before/after diff is exact by construction.
- The agent does the work on the node. The manifest stays on the node, matching
  the existing check model. No controller-side component and no shipping of
  observed files to nodes.
- One wrapper play imports the existing cookbooks. Their recipes are never
  edited.
- The record commands reuse existing code: hashing, package/service snapshot,
  and the manifest JSON. Schema stays 1; file entries gain size and mtime.
- Deliberately not included in v1: a callback plugin (controller-side, misses
  `command`/`shell` writes, adds a controller-to-node shipping step), tracking
  every file on disk (OS noise), and silently auto-adopting changes into the
  baseline. A run with no edits must not grow the manifest.

## Adoption flow

1. Add the wrapper play and the two record commands around the real cookbooks.
2. Run the wrapper once per host type. The manifest now tracks exactly what
   those cookbooks manage.
3. Deploy the agent as a service. It checks the manifest on the audit interval
   and reports to OTel.
4. When a cookbook adds a file, run the wrapper again. `record-end` extends the
   manifest.

## Scope limits

`record-end` captures what the run changed under the chosen roots. Files the
cookbook writes elsewhere are out of scope, and anything not recorded is
invisible. A node state change made after provisioning is drift, which is the
point of checking.