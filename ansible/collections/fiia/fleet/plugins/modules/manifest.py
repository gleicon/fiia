#!/usr/bin/python
# -*- coding: utf-8 -*-

DOCUMENTATION = r"""
---
module: manifest
short_description: Generate a fiia drift-detection manifest on the managed node
description:
  - Hashes declared files, queries installed package versions, and checks
    service states on the managed node.
  - Writes the result to a JSON manifest file that the fiia-agent reads
    periodically to detect configuration drift without spawning ansible.
  - Idempotent: reports changed=true only when manifest content changes
    (generated_at timestamp is excluded from the comparison).
  - Run this as the last task in your provisioning play so the manifest
    captures post-apply desired state.
options:
  dest:
    description: Path to write the manifest JSON file.
    type: str
    default: /etc/fiia/manifest.json
  files:
    description: List of file paths to track (absolute paths).
    type: list
    elements: str
    default: []
  packages:
    description: >
      List of packages to track. Each element is either a string (package name)
      or a dict with keys C(name) and optional C(version).
    type: list
    elements: raw
    default: []
  services:
    description: >
      List of services to track. Each element is either a string (service name)
      or a dict with keys C(name), optional C(running) (bool, default true),
      and optional C(enabled) (bool, default true).
    type: list
    elements: raw
    default: []
requirements:
  - dpkg-query (Debian/Ubuntu) or rpm (RHEL/CentOS) for package checks
  - systemctl for service checks
author:
  - Fiia project
"""

EXAMPLES = r"""
- name: Update fiia drift manifest
  fiia.fleet.manifest:
    dest: /etc/fiia/manifest.json
    files:
      - /etc/nginx/nginx.conf
      - /etc/ssh/sshd_config
    packages:
      - nginx
      - { name: openssh-server }
    services:
      - nginx
      - { name: ssh, running: true, enabled: true }
"""

RETURN = r"""
manifest_path:
  description: Path the manifest was written to.
  type: str
  returned: always
deviations:
  description: >
    Non-empty list of warning strings when declared items could not be
    resolved (missing packages, unreadable files). Does not cause failure.
  type: list
  returned: when warnings exist
"""

import hashlib
import json
import os
import stat
import subprocess
import time

from ansible.module_utils.basic import AnsibleModule


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, 'rb') as fh:
        for chunk in iter(lambda: fh.read(65536), b''):
            h.update(chunk)
    return h.hexdigest()


def query_package_version(name):
    """Return installed version string or None. Tries dpkg-query then rpm."""
    try:
        out = subprocess.check_output(
            ['dpkg-query', '-W', '-f=${Version}', name],
            stderr=subprocess.DEVNULL,
        ).decode().strip()
        if out:
            return out
    except (subprocess.CalledProcessError, FileNotFoundError):
        pass

    try:
        out = subprocess.check_output(
            ['rpm', '-q', '--qf', '%{VERSION}-%{RELEASE}', name],
            stderr=subprocess.DEVNULL,
        ).decode().strip()
        if out and 'not installed' not in out:
            return out
    except (subprocess.CalledProcessError, FileNotFoundError):
        pass

    return None


def service_states(name):
    """Return (running: bool, enabled: bool). Both False on error."""
    def run(cmd):
        return subprocess.call(cmd, stdout=subprocess.DEVNULL,
                               stderr=subprocess.DEVNULL) == 0

    running = run(['systemctl', 'is-active', '--quiet', name])
    enabled = run(['systemctl', 'is-enabled', '--quiet', name])
    return running, enabled


def build_manifest(params):
    """Build manifest dict from module params. Returns (manifest, warnings)."""
    manifest = {
        'schema_version': 1,
        'generated_at': int(time.time()),
        'files': [],
        'packages': [],
        'services': [],
    }
    warnings = []

    for path in params['files']:
        if not os.path.isfile(path):
            warnings.append('file not found at provisioning time: {}'.format(path))
            continue
        s = os.stat(path)
        manifest['files'].append({
            'path': path,
            'sha256': sha256_file(path),
            'mode': '{:o}'.format(stat.S_IMODE(s.st_mode)),
        })

    for pkg in params['packages']:
        name = pkg if isinstance(pkg, str) else pkg['name']
        declared_version = None if isinstance(pkg, str) else pkg.get('version')
        installed = query_package_version(name)
        if installed is None:
            warnings.append('package not installed at provisioning time: {}'.format(name))
        entry = {'name': name}
        # Store declared version if given, else installed version.
        version = declared_version or installed
        if version:
            entry['version'] = version
        manifest['packages'].append(entry)

    for svc in params['services']:
        if isinstance(svc, str):
            name, want_running, want_enabled = svc, True, True
        else:
            name = svc['name']
            want_running = svc.get('running', True)
            want_enabled = svc.get('enabled', True)
        running, enabled = service_states(name)
        manifest['services'].append({
            'name': name,
            'running': running,
            'enabled': enabled,
        })
        if want_running and not running:
            warnings.append('service not active at provisioning time: {}'.format(name))
        if want_enabled and not enabled:
            warnings.append('service not enabled at provisioning time: {}'.format(name))

    return manifest, warnings


def manifests_equal(a, b):
    """Compare two manifests ignoring generated_at."""
    def strip(m):
        c = dict(m)
        c.pop('generated_at', None)
        return c
    return json.dumps(strip(a), sort_keys=True) == json.dumps(strip(b), sort_keys=True)


def main():
    module = AnsibleModule(
        argument_spec=dict(
            dest=dict(type='str', default='/etc/fiia/manifest.json'),
            files=dict(type='list', elements='str', default=[]),
            packages=dict(type='list', elements='raw', default=[]),
            services=dict(type='list', elements='raw', default=[]),
        ),
        supports_check_mode=True,
    )

    dest = module.params['dest']
    manifest, warnings = build_manifest(module.params)

    # Read existing manifest to determine changed (ignore generated_at).
    changed = True
    try:
        with open(dest) as fh:
            existing = json.load(fh)
        changed = not manifests_equal(existing, manifest)
    except Exception:
        changed = True

    if changed and not module.check_mode:
        dest_dir = os.path.dirname(dest)
        if dest_dir:
            os.makedirs(dest_dir, exist_ok=True)
        tmp = dest + '.tmp'
        with open(tmp, 'w') as fh:
            json.dump(manifest, fh, indent=2)
        os.replace(tmp, dest)
        os.chmod(dest, 0o400)

    result = dict(changed=changed, manifest_path=dest)
    if warnings:
        result['warnings'] = warnings
    module.exit_json(**result)


if __name__ == '__main__':
    main()
