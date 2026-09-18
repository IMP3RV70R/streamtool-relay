#!/usr/bin/env python3
"""Fixed root host preparation protocol; never handles SSH/owner credentials.

This prepares prerequisites, not an application installation. Signed release
staging, IP certificates and owner setup are separate subsequent operations.
"""
import contextlib
import errno
import fcntl
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path('/var/lib/streamtool-installer')
HELPER = Path('/usr/local/libexec/streamtool-installer.py')
SERVICE = Path('/etc/systemd/system/streamtool-installer.service')
TIMER = Path('/etc/systemd/system/streamtool-installer.timer')
TIMER_UNIT = '''[Unit]
Description=Resume streamtool-relay host preparation
[Timer]
OnActiveSec=10
OnUnitInactiveSec=30
Unit=streamtool-installer.service
[Install]
WantedBy=timers.target
'''
UNIT = '''[Unit]
Description=streamtool-relay host preparation
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=2700
StartLimitBurst=3
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 /usr/local/libexec/streamtool-installer.py run
TimeoutStartSec=45min
Restart=on-failure
RestartSec=20
UMask=0077
[Install]
WantedBy=multi-user.target
'''


def command(*args, timeout=900):
    # Do not collect provider/package output in a public status or exception.
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL,
                   stderr=subprocess.DEVNULL, timeout=timeout,
                   env=dict(os.environ, DEBIAN_FRONTEND='noninteractive'))


def atomic(path, data, mode=0o600):
    fd, temporary = tempfile.mkstemp(prefix='.installer-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as file:
            os.fchmod(file.fileno(), mode); file.write(data); file.flush(); os.fsync(file.fileno())
        os.replace(temporary, path)
        fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try: os.fsync(fd)
        finally: os.close(fd)
    finally:
        if os.path.exists(temporary): os.unlink(temporary)


class Journal:
    def __init__(self, root): self.root = root
    @contextlib.contextmanager
    def lock(self):
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        info = self.root.lstat()
        if not self.root.is_dir() or self.root.is_symlink() or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise ValueError('unsafe journal directory')
        fd = os.open(self.root/'lock', os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            yield
        finally: os.close(fd)
    def read(self):
        try: fd = os.open(self.root/'state.json', os.O_RDONLY | os.O_NOFOLLOW)
        except FileNotFoundError: return None
        with os.fdopen(fd, 'rb') as file:
            data = file.read(8193)
        if len(data) > 8192: raise ValueError('oversized journal')
        state = json.loads(data)
        if (state.get('protocol') != 1 or state.get('phase') not in
            ['PENDING','PREREQUISITES','DOCKER','VERIFY','PREPARED','FAILED']
            or not isinstance(state.get('attempts'), int) or not 0 <= state['attempts'] <= 3
            or not isinstance(state.get('deadline'), int) or not isinstance(state.get('docker_managed'),bool)):
            raise ValueError('invalid journal')
        uuid.UUID(state['job_id'])
        return state
    def write(self, state): atomic(self.root/'state.json', (json.dumps(state)+'\n').encode())


def host_checks():
    system = {}
    for line in Path('/etc/os-release').read_text().splitlines():
        if '=' in line:
            key, value = line.split('=', 1); system[key] = value.strip('"')
    pair = (system.get('ID'), system.get('VERSION_ID'))
    if pair not in [('debian','13'),('ubuntu','24.04')]: raise ValueError('unsupported_os')
    if platform.machine() not in ['aarch64','x86_64']: raise ValueError('unsupported_architecture')
    if not Path('/run/systemd/system').is_dir(): raise ValueError('systemd_required')
    if Path('/opt/streamtool').exists() or Path('/etc/streamtool').exists(): raise ValueError('existing_installation')
    if shutil.disk_usage('/').free < 8 << 30: raise ValueError('insufficient_disk')
    memory = int(next(line.split()[1] for line in Path('/proc/meminfo').read_text().splitlines() if line.startswith('MemTotal:'))) * 1024
    if memory < 1536 << 20 or (os.cpu_count() or 0) < 2: raise ValueError('insufficient_resources')
    for family, address in [(socket.AF_INET,'0.0.0.0'),(socket.AF_INET6,'::')]:
        for port, kind in [(80,socket.SOCK_STREAM),(443,socket.SOCK_STREAM),(443,socket.SOCK_DGRAM),(1935,socket.SOCK_STREAM),(8890,socket.SOCK_DGRAM)]:
            try:
                with socket.socket(family, kind) as sock:
                    if family == socket.AF_INET6: sock.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1)
                    sock.bind((address,port))
            except OSError as error:
                if family == socket.AF_INET6 and error.errno in [errno.EAFNOSUPPORT,errno.EPROTONOSUPPORT]: continue
                raise ValueError('occupied_port') from None
    return system['ID'], 'trixie' if pair[0] == 'debian' else 'noble'


class Preparation:
    def __init__(self, journal, check=host_checks, execute=command):
        self.journal, self.check, self.execute = journal, check, execute
    def begin(self):
        with self.journal.lock():
            state = self.journal.read()
            if state and state['phase'] != 'FAILED': return state
            self.check()
            managed = state.get('docker_managed', False) if state else not bool(shutil.which('docker'))
            if not state:
                if managed:
                    for package in ['docker.io','podman-docker','containerd','runc','docker-ce','docker-ce-cli','containerd.io']:
                        found = subprocess.run(['dpkg-query','-W','-f=${db:Status-Status}',package],capture_output=True,text=True)
                        if found.returncode == 0 and found.stdout == 'installed': raise ValueError('existing_container_runtime')
                    for file in Path('/etc/apt/sources.list.d').glob('*'):
                        if file.is_file() and 'download.docker.com' in file.read_text(errors='replace'): raise ValueError('existing_docker_repository')
                else:
                    self.execute('docker','info',timeout=30)
                    self.execute('docker','compose','version',timeout=30)
            state = {'protocol':1,'job_id':state['job_id'] if state else str(uuid.uuid4()),
                     'phase':'PENDING','attempts':0,'deadline':int(time.time())+2700,'error':None,'docker_managed':managed}
            self.journal.write(state)
            return state
    def run(self, steps):
        with self.journal.lock():
            state = self.journal.read()
            if state is None: raise ValueError('missing job')
            if state['phase'] in ['PREPARED','FAILED']: return state
            if state['attempts'] >= 3 or int(time.time()) >= state['deadline']:
                state.update(phase='FAILED',error='recovery_budget_exhausted');self.journal.write(state);return state
            state['attempts'] += 1; self.journal.write(state)
            try:
                self.check()
                phases = [name for name, _ in steps]
                start = 0 if state['phase'] == 'PENDING' else phases.index(state['phase'])
                for phase, action in steps[start:]:
                    if int(time.time()) >= state['deadline']: raise TimeoutError()
                    state.update(phase=phase,error=None);self.journal.write(state)
                    action()
                state.update(phase='PREPARED',error=None);self.journal.write(state)
            except Exception:
                # Preserve phase for bounded automatic retry; report no raw errors.
                if state['attempts'] >= 3: state['phase'] = 'FAILED'
                state['error'] = 'preparation_failed';self.journal.write(state)
                raise
            return state


def steps(managed):
    def prerequisites():
        command('apt-get','update')
        command('apt-get','install','-y','--no-install-recommends','ca-certificates','curl','gnupg','openssl','python3-venv','iptables','iproute2','util-linux','sudo')
    def docker():
        system, codename = host_checks()
        if not managed:
            command('docker','info',timeout=30);command('docker','compose','version',timeout=30)
            return  # Existing Docker is never replaced/upgraded.
        # Refuse competing installations rather than uninstalling someone else's packages.
        key = Path('/etc/apt/keyrings/streamtool-docker.asc')
        source = Path('/etc/apt/sources.list.d/streamtool-docker.sources')
        # Another Docker source requires administration; do not create conflicting Signed-By entries.
        for file in Path('/etc/apt/sources.list.d').glob('*'):
            if file != source and file.is_file() and 'download.docker.com' in file.read_text(errors='replace'):
                raise ValueError('existing_docker_repository')
        key.parent.mkdir(mode=0o755,parents=True,exist_ok=True)
        with tempfile.TemporaryDirectory(dir=key.parent) as tmp:
            downloaded = Path(tmp)/'key.asc'
            command('curl','--fail','--silent','--show-error','--proto','=https','--max-time','90',f'https://download.docker.com/linux/{system}/gpg','-o',str(downloaded),timeout=100)
            result = subprocess.check_output(['gpg','--batch','--show-keys','--with-colons',str(downloaded)],stderr=subprocess.DEVNULL,text=True)
            fingerprints = [line.split(':')[9] for line in result.splitlines() if line.startswith('fpr:')]
            if not fingerprints or fingerprints[0] != '9DC858229FC7DD38854AE2D88D81803C0EBFCD88': raise ValueError('docker_key_changed')
            atomic(key,downloaded.read_bytes(),0o644)
        arch = 'arm64' if platform.machine() == 'aarch64' else 'amd64'
        data = f'Types: deb\nURIs: https://download.docker.com/linux/{system}\nSuites: {codename}\nComponents: stable\nArchitectures: {arch}\nSigned-By: {key}\n'
        atomic(source,data.encode(),0o644)
        command('apt-get','update')
        command('apt-get','install','-y','--no-install-recommends','docker-ce','docker-ce-cli','containerd.io','docker-compose-plugin')
        command('systemctl','enable','--now','docker')
    def verify():
        for tool in ['docker','python3','openssl','iptables','ip6tables','tc','nsenter','unshare','sudo','visudo']:
            if not shutil.which(tool): raise ValueError('missing_tool')
        command('docker','info',timeout=30);command('docker','compose','version',timeout=30)
        command('unshare','--net','--','sh','-c',
                'ip link add stprobe type dummy && tc qdisc add dev stprobe root tbf rate 1mbit burst 16kb latency 100ms && iptables -N STREAMTOOL_PROBE && ip6tables -N STREAMTOOL_PROBE',timeout=30)
    return [('PREREQUISITES',prerequisites),('DOCKER',docker),('VERIFY',verify)]


def main():
    if os.geteuid() != 0 or len(sys.argv) != 2 or sys.argv[1] not in ['prepare','status','run']: raise ValueError('invalid invocation')
    journal = Journal(ROOT); preparation = Preparation(journal)
    if sys.argv[1] == 'status':
        # Atomic replacement allows observation while the autonomous runner owns its lock.
        print(json.dumps(journal.read() or {'protocol':1,'phase':'NOT_STARTED'}));return
    if sys.argv[1] == 'prepare':
        host_checks()
        for path, expected in [(SERVICE,UNIT),(TIMER,TIMER_UNIT)]:
            if path.is_symlink() or path.exists() and path.read_text() != expected:
                raise ValueError('existing_service_conflict')
        # Install the independent wake source before committing a new job. A lost
        # SSH response cannot strand PENDING between journaling and service start.
        atomic(SERVICE,UNIT.encode(),0o644)
        atomic(TIMER,TIMER_UNIT.encode(),0o644)
        command('systemctl','daemon-reload',timeout=30)
        command('systemctl','enable','streamtool-installer.service',timeout=30)
        command('systemctl','enable','--now','streamtool-installer.timer',timeout=30)
        state = preparation.begin()
        if state['phase'] != 'PREPARED':
            command('systemctl','reset-failed','streamtool-installer.service',timeout=30)
            command('systemctl','start','--no-block','streamtool-installer.service',timeout=30)
        print(json.dumps(state));return
    state = journal.read()
    if state is None: return
    if state['phase'] not in ['PREPARED','FAILED']:
        state = preparation.run(steps(state['docker_managed']))
    if state['phase'] in ['PREPARED','FAILED']:
        command('systemctl','disable','--now','streamtool-installer.timer',timeout=30)


if __name__ == '__main__':
    try: main()
    except Exception:
        print('{"protocol":1,"error":"host_preparation_refused"}',file=sys.stderr)
        sys.exit(1)
