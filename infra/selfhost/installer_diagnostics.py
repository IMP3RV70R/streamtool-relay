#!/usr/bin/env python3
"""Read-only bounded host observations. Never return command output or secrets."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess


def service(unit):
    result = {}
    fields = ('ActiveState', 'SubState', 'Result', 'ExecMainStatus')
    try:
        args = ['systemctl', 'show', unit] + ['--property=' + field for field in fields]
        output = subprocess.check_output(args, stderr=subprocess.DEVNULL, timeout=3).decode()
        for line in output.splitlines():
            field, _, value = line.partition('=')
            if field in fields and re.fullmatch(r'[a-z0-9-]{1,40}', value): result[field] = value
    except (OSError, subprocess.SubprocessError): pass
    return result


def collect():
    result = {'protocol': 1, 'installation_service': service('streamtool-installation.service'),
              'docker_service': service('docker.service')}
    for name, path in [('installation', '/var/lib/streamtool-installer'), ('docker', '/var/lib/docker')]:
        try:
            usage = shutil.disk_usage(path)
            stats = os.statvfs(path)
            result[name + '_disk_free_bytes'] = usage.free
            result[name + '_free_inodes'] = stats.f_favail
        except OSError: pass
    try:
        for line in Path('/proc/meminfo').read_text().splitlines():
            key, _, value = line.partition(':')
            if key in ('MemTotal', 'MemAvailable', 'SwapFree'):
                result[key] = int(value.strip().split()[0]) * 1024
    except (OSError, ValueError): pass
    try:
        subprocess.run(['docker', 'info', '--format', '{{.ServerVersion}}'], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        result['docker_available'] = True
    except (OSError, subprocess.SubprocessError): result['docker_available'] = False
    return result


if __name__ == '__main__':
    print(json.dumps(collect()))
