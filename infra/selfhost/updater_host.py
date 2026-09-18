#!/usr/bin/env python3
"""Fixed Linux operations for the independent updater; no shell execution."""
import base64
import contextlib
import hashlib
import http.client
import json
import os
import platform
from pathlib import Path
import shutil
import socket
import sqlite3
import ssl
import subprocess
import stat
import uuid
import time

import backup
from maintenance import sync_directory

ROOT_FILES = ('.env', 'api.env', 'node.env', 'worker-network.env', 'keyring.json', 'media-node.json', 'compose.yml', 'Caddyfile', 'mediamtx.yml', 'VERSION', 'RELEASE_SEQUENCE')
HOST_FILES = {
    'update.sh': Path('/usr/local/libexec/streamtool-maintenance/update.sh'),
    'node-agent': Path('/usr/local/bin/node-agent'),
    'streamtool-worker-policy': Path('/usr/local/libexec/streamtool-worker-policy'),
    'streamtool-agent.service': Path('/etc/systemd/system/streamtool-agent.service'),
    'streamtool-agent.sudoers': Path('/etc/sudoers.d/streamtool-agent'),
    'streamtool-certificates.service': Path('/etc/systemd/system/streamtool-certificates.service'),
    'streamtool-certificates.timer': Path('/etc/systemd/system/streamtool-certificates.timer'),
    'backup.py': Path('/usr/local/libexec/streamtool-maintenance/backup.py'),
    'renew.py': Path('/usr/local/libexec/streamtool-maintenance/renew.py'),
    'maintenance.py': Path('/usr/local/libexec/streamtool-maintenance/maintenance.py'),
}
IMAGE_KEYS = {'API_IMAGE':'api-image', 'WORKER_IMAGE':'worker-image', 'PROXY_IMAGE':'proxy-image', 'EDGE_IMAGE':'edge-image'}


def command(*arguments, timeout=180):
    # Never copy command output/errors into journals or user-visible diagnostics.
    result = subprocess.run(arguments, check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=timeout)
    if len(result.stdout) > 2 << 20:
        raise ValueError('host response too large')
    return result.stdout.decode().strip()


def settings(path):
    return dict(line.split('=', 1) for line in path.read_text().splitlines() if line and not line.startswith('#'))


def atomic_copy(source, destination, mode=0o600):
    temporary = destination.with_name(destination.name + '.updater-new')
    with source.open('rb') as incoming, temporary.open('wb') as output:
        shutil.copyfileobj(incoming, output)
        output.flush()
        os.fsync(output.fileno())
    temporary.chmod(mode)
    with temporary.open('rb') as output: os.fsync(output.fileno())
    os.replace(temporary, destination)
    sync_directory(destination.parent)


def tree_manifest(directory):
    result = {}
    for path in sorted(directory.rglob('*')):
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError('recovery set contains unsupported files')
        if path.is_file() and path != directory / 'manifest.json':
            with path.open('rb') as incoming:
                result[path.relative_to(directory).as_posix()] = hashlib.file_digest(incoming, 'sha256').hexdigest()
    return result


def seal(directory):
    from maintenance import durable_json
    durable_json(directory / 'manifest.json', tree_manifest(directory))
    for path in directory.rglob('*'):
        if path.is_file():
            with path.open('rb') as incoming:
                os.fsync(incoming.fileno())
    for path in sorted((path for path in directory.rglob('*') if path.is_dir()), reverse=True):
        sync_directory(path)
    sync_directory(directory)


def verify_recovery(directory):
    if tree_manifest(directory) != json.loads((directory / 'manifest.json').read_text()):
        raise ValueError('recovery set checksum mismatch')
    with __import__('tarfile').open(directory / 'database.tar.gz') as archive:
        # Verify the same trusted snapshot inventory before mutation/rollback.
        expected = json.load(archive.extractfile('manifest.json'))
        actual = {}
        for member in archive.getmembers():
            if member.isfile() and member.name != 'manifest.json':
                actual[member.name] = hashlib.file_digest(archive.extractfile(member), 'sha256').hexdigest()
        if actual != expected:
            raise ValueError('snapshot checksum mismatch')


class DiskReserve:
    """Root-owned, physically allocated, per-job space; never application data."""
    def __init__(self, directory):
        self.directory = Path(directory)
        self.inventory = self.directory / 'reserve.json'

    def allocate(self, anchors, size):
        from maintenance import durable_json
        job_id = str(uuid.UUID(self.directory.name))
        if job_id != self.directory.name or self.inventory.exists():
            raise ValueError('invalid or existing recovery reserve')
        entries, devices = [], set()
        for anchor in anchors:
            anchor = Path(anchor)
            device = anchor.stat().st_dev
            if device in devices: continue
            devices.add(device)
            location = anchor / '.streamtool-recovery-space'
            location.mkdir(mode=0o700, exist_ok=True)
            metadata = location.lstat()
            if not stat.S_ISDIR(metadata.st_mode) or metadata.st_uid != os.geteuid() or metadata.st_mode & 0o077:
                raise ValueError('unsafe recovery reserve directory')
            sync_directory(anchor)
            if shutil.disk_usage(location).free < size * 2:
                raise ValueError('insufficient allocated recovery headroom')
            entries.append({'path':str(location / (job_id + '.bin')), 'device':device})
        # Record all destinations before allocation: interruption/partial failure
        # cannot leave an untracked reservation. No client controls these paths.
        durable_json(self.inventory, {'size':size, 'entries':entries})
        for entry in entries:
            path = Path(entry['path'])
            parent = self.open_parent(path, entry['device'])
            try:
                descriptor = os.open(path.name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
                try:
                    os.posix_fallocate(descriptor, 0, size)
                    os.fsync(descriptor)
                    if os.fstat(descriptor).st_blocks * 512 < size:
                        raise ValueError('recovery reserve not physically allocated')
                finally: os.close(descriptor)
                os.fsync(parent)
            finally: os.close(parent)

    @staticmethod
    def open_parent(path, device):
        descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        metadata = os.fstat(descriptor)
        if metadata.st_uid != os.geteuid() or metadata.st_mode & 0o077 or metadata.st_dev != device:
            os.close(descriptor)
            raise ValueError('unsafe recovery reserve directory')
        return descriptor

    def release(self, force=False):
        if not self.inventory.exists(): return False
        with self.inventory.open('rb') as incoming:
            data = incoming.read(16385)
        if len(data) > 16384: raise ValueError('invalid reserve inventory')
        inventory = json.loads(data)
        if not isinstance(inventory['entries'], list) or len(inventory['entries']) > 8:
            raise ValueError('invalid reserve inventory')
        released = False
        for entry in inventory['entries']:
            path = Path(entry['path'])
            if path.name != self.directory.name + '.bin' or path.parent.name != '.streamtool-recovery-space':
                raise ValueError('invalid reserve path')
            parent = self.open_parent(path, entry['device'])
            try:
                try: descriptor = os.open(path.name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=parent)
                except FileNotFoundError: continue
                try:
                    metadata = os.fstat(descriptor)
                    if not stat.S_ISREG(metadata.st_mode) or metadata.st_uid != os.geteuid() or metadata.st_mode & 0o077 or metadata.st_nlink != 1 or metadata.st_dev != entry['device']:
                        raise ValueError('unsafe recovery reserve file')
                    capacity = os.fstatvfs(parent)
                    if not force and capacity.f_bfree * capacity.f_frsize >= 64 << 20: continue
                finally: os.close(descriptor)
                # Directory FD keeps both validation and unlink on the same
                # directory even if an untrusted ancestor is renamed concurrently.
                os.unlink(path.name, dir_fd=parent)
                os.fsync(parent)
                released = True
            finally: os.close(parent)
        return released


class LinuxHost:
    def __init__(self, root, directory):
        self.root = Path(root)
        self.directory = Path(directory)
        self.recovery = self.directory / 'recovery'
        self.stage = self.directory / 'stage'

    def compose(self, *arguments):
        return command('docker', 'compose', '--project-directory', str(self.root), '-f', str(self.root / 'compose.yml'), *arguments, timeout=900)

    def owned_workers(self):
        return command('docker', 'ps', '-aq', '--filter', 'label=streamtool.node=00000000-0000-4000-8000-000000000001')

    def installed(self):
        with contextlib.closing(sqlite3.connect((self.root / 'state/control.sqlite').as_uri() + '?mode=ro', uri=True)) as database, database:
            schema = database.execute('SELECT count(*) FROM schema_migrations').fetchone()[0]
        return {'version':(self.root / 'VERSION').read_text().strip(), 'sequence':int((self.root / 'RELEASE_SEQUENCE').read_text()), 'schema':schema, 'worker_image':settings(self.root / '.env')['WORKER_IMAGE']}

    def idle(self, job):
        manifest = json.loads((self.directory / 'manifest.json').read_text())
        if hashlib.sha256((self.directory / 'manifest.json').read_bytes()).hexdigest() != job['digest']:
            raise ValueError('job manifest mismatch')
        architecture = {'x86_64':'amd64','aarch64':'arm64','arm64':'arm64'}[platform.machine()]
        if (self.stage / 'ARCHITECTURE').read_text().strip() != architecture or (self.stage / 'VERSION').read_text().strip() != job['target']['version'] or (self.stage / 'worker-image').read_text().strip() != job['target']['worker_image'] or manifest['version'] != job['target']['version'] or manifest['sequence'] != job['target']['sequence'] or manifest['target_schema'] != job['target']['schema']:
            raise ValueError('target metadata mismatch')
        # Admission is exclusively held by the coordinator. Capture the fresh
        # idle observation before slow verification; media mutations remain fenced.
        with contextlib.closing(sqlite3.connect((self.root / 'state/control.sqlite').as_uri() + '?mode=ro', uri=True)) as database, database:
            for query in ("SELECT count(*) FROM ingest_connections WHERE status='CONNECTED'", "SELECT count(*) FROM stream_sessions WHERE phase NOT IN ('ENDED','FAILED')", "SELECT count(*) FROM worker_allocations WHERE state NOT IN ('STOPPED','FAILED')"):
                if database.execute(query).fetchone()[0]:
                    raise ValueError('broadcast active')
            health = database.execute('SELECT last_seen_at FROM edge_observation_health WHERE edge_id=?', (settings(self.root / 'api.env')['EDGE_ID'],)).fetchone()
            if not health or database.execute("SELECT julianday('now')-julianday(?) BETWEEN 0 AND 6.0/86400", (health[0],)).fetchone()[0] != 1:
                raise ValueError('missing or stale publisher observation')
        command('/usr/local/libexec/streamtool-updater/release-tool', 'verify', '--metadata-only', '--trust', '/etc/streamtool/release-trust.json', '--state', str(self.directory.parent / 'watermark.json'), '--manifest', str(self.directory / 'manifest.json'), '--signature', str(self.directory / 'manifest.sig'), '--architecture', architecture, '--installed-sequence', str(job['old']['sequence']), '--installed-schema', str(job['old']['schema']))
        checksums = dict((line[66:],line[:64]) for line in (self.stage / 'SHA256SUMS').read_text().splitlines())
        actual = {}
        for path in self.stage.rglob('*'):
            if path.is_symlink(): raise ValueError('staged target link')
            if path.is_file() and path != self.stage / 'SHA256SUMS':
                with path.open('rb') as incoming:
                    actual[path.relative_to(self.stage).as_posix()] = hashlib.file_digest(incoming,'sha256').hexdigest()
        if actual != checksums or not all((self.stage / name).is_file() for name in HOST_FILES):
            raise ValueError('staged target integrity mismatch')
        api_settings = settings(self.root / 'api.env')
        if api_settings.get('MAINTENANCE_DIRECTORY') != '/maintenance' or settings(self.root / 'node.env').get('MAINTENANCE_DIRECTORY') != '/var/lib/streamtool-updater/admission':
            raise ValueError('maintenance admission not configured')
        container = self.compose('ps', '-q', 'api')
        mounts = json.loads(command('docker', 'inspect', '--format', '{{json .Mounts}}', container))
        if not any(mount['Source']=='/var/lib/streamtool-updater/admission' and mount['Destination']=='/maintenance' and not mount['RW'] for mount in mounts):
            raise ValueError('shared admission volume unavailable')
        self.health(job, old=True)
        if self.installed() != job['old'] or self.owned_workers():
            raise ValueError('installation changed or workers present')
        # Compose validation uses private copies with target images, never mutates root.
        planned = self.directory / 'planned'
        planned.mkdir(mode=0o700)
        for name in ('compose.yml', 'Caddyfile', 'mediamtx.yml'):
            shutil.copyfile(self.stage / name, planned / name)
        shutil.copyfile(self.root / 'api.env', planned / 'api.env')
        values = settings(self.root / '.env')
        values.update({key:(self.stage / name).read_text().strip() for key,name in IMAGE_KEYS.items()})
        (planned / '.env').write_text(''.join(f'{key}={value}\n' for key,value in values.items()))
        config = json.loads(command('docker', 'compose', '--project-directory', str(planned), '-f', str(planned / 'compose.yml'), 'config', '--format', 'json'))
        if config.get('name') != 'streamtool-selfhost' or set(config['services']) != {'api','edge','proxy'}:
            raise ValueError('unsupported installation service contract')
        # Conservative reserve for old images, DB/files and both interrupted states.
        images = [command('docker', 'image', 'inspect', '--format', '{{.Size}}', settings(self.root / '.env')[key]) for key in IMAGE_KEYS]
        required = sum(map(int, images)) * 2 + sum(path.stat().st_size for path in self.root.rglob('*') if path.is_file()) * 3 + (2 << 30)
        if shutil.disk_usage(self.directory).free < required * 2:
            raise ValueError('insufficient recovery disk reserve')
        self.reserve(required)

    def reserve(self, size):
        docker = Path(command('docker', 'info', '--format', '{{.DockerRootDir}}'))
        if not docker.is_absolute() or not docker.is_dir():
            raise ValueError('invalid Docker storage location')
        DiskReserve(self.directory).allocate((self.directory.parent, self.root,
            docker, Path('/etc'), Path('/usr/local'), Path('/var/lib/streamtool')), size)

    def prepare(self, job):
        self.recovery.mkdir(mode=0o700)
        self.compose('stop', 'api')
        backup.snapshot(self.root, self.recovery / 'database.tar.gz')
        root_copy = self.recovery / 'root'
        root_copy.mkdir(mode=0o700)
        for name in ROOT_FILES:
            shutil.copyfile(self.root / name, root_copy / name)
        shutil.copytree(self.root / 'web', root_copy / 'web')
        host_copy = self.recovery / 'host'
        host_copy.mkdir(mode=0o700)
        for name,destination in HOST_FILES.items():
            shutil.copyfile(destination, host_copy / name)
        values = settings(self.root / '.env')
        images = [values[key] for key in IMAGE_KEYS]
        # Keep an offline OCI recovery archive, not only mutable tags/registry refs.
        for image in images:
            if not image.startswith('sha256:') or len(image) != 71:
                raise ValueError('installed images must use immutable IDs')
            if command('docker', 'image', 'inspect', '--format', '{{.Id}}', image) != image:
                raise ValueError('installed image unavailable')
        command('docker', 'save', '-o', str(self.recovery / 'images.tar'), *images, timeout=900)
        seal(self.recovery)
        verify_recovery(self.recovery)

    def stop(self):
        if self.owned_workers():
            raise ValueError('unexpected worker during maintenance')
        command('systemctl', 'stop', 'streamtool-agent')
        # Teardown must work even when target configuration/startup is broken.
        # Select only this installation's stable Compose ownership label.
        containers = command('docker','ps','-aq','--filter','label=com.docker.compose.project=streamtool-selfhost').split()
        if containers:
            command('docker','stop','-t','30',*containers)
            command('docker','rm',*containers)

    def start(self):
        backup.normalize(self.root)
        command('systemctl', 'daemon-reload')
        self.compose('up', '-d', '--pull', 'never')
        command('systemctl', 'start', 'streamtool-agent')

    def abort(self, job):
        DiskReserve(self.directory).release()
        # Preparation changes no application files/schema; restarting is safe.
        self.start()

    def install(self, job):
        command('docker', 'load', '-i', str(self.stage / 'images.tar'), timeout=900)
        self.stop()
        for name in ('.env', 'api.env', 'node.env'):
            values = settings(self.root / name)
            if name == '.env':
                values.update({key:(self.stage / source).read_text().strip() for key,source in IMAGE_KEYS.items()})
            else:
                values['WORKER_IMAGE'] = job['target']['worker_image']
            temporary = self.directory / ('new-' + name.lstrip('.'))
            temporary.write_text(''.join(f'{key}={value}\n' for key,value in values.items()))
            atomic_copy(temporary, self.root / name)
        for name in ('compose.yml', 'Caddyfile', 'mediamtx.yml', 'VERSION'):
            atomic_copy(self.stage / name, self.root / name, 0o644)
        sequence = self.directory / 'target-sequence'
        sequence.write_text(str(job['target']['sequence']) + '\n')
        atomic_copy(sequence, self.root / 'RELEASE_SEQUENCE', 0o644)
        self.replace_web(self.stage / 'web', 'previous-web')
        for name,destination in HOST_FILES.items():
            atomic_copy(self.stage / name, destination, 0o755 if name in ('node-agent','streamtool-worker-policy','update.sh') or name.endswith('.py') else 0o440 if name.endswith('sudoers') else 0o644)
        atomic_copy(self.root / 'node.env', Path('/etc/streamtool/node.env'))
        command('visudo', '-cf', '/etc/sudoers.d/streamtool-agent')
        self.start()

    def replace_web(self, source, previous):
        temporary = self.root / 'web.updater-new'
        if temporary.exists():
            shutil.rmtree(temporary)
        shutil.copytree(source, temporary)
        for path in [temporary, *temporary.rglob('*')]:
            path.chmod(0o755 if path.is_dir() else 0o644)
        for path in temporary.rglob('*'):
            if path.is_file():
                with path.open('rb') as output: os.fsync(output.fileno())
        for path in sorted((path for path in temporary.rglob('*') if path.is_dir()),reverse=True):
            sync_directory(path)
        sync_directory(temporary)
        if (self.root / 'web').exists():
            # Keep rename on the website filesystem: /var/lib may be a separate
            # volume from /opt. Recovery must not depend on cross-device rename.
            diagnostic = self.root / ('web.updater-' + previous + '-' + str(time.time_ns()))
            (self.root / 'web').rename(diagnostic)
        temporary.rename(self.root / 'web')
        sync_directory(self.root)

    def restore(self, job):
        DiskReserve(self.directory).release()
        verify_recovery(self.recovery)
        self.stop()
        # Never depend on registry or old image tags surviving a prune.
        command('docker', 'load', '-i', str(self.recovery / 'images.tar'), timeout=900)
        failed = self.directory / 'failed-installation'
        if not failed.exists():
            staging = self.directory / 'failed-installation-new'
            if staging.exists(): shutil.rmtree(staging)
            staging.mkdir(mode=0o700)
            shutil.copytree(self.root / 'state', staging / 'state')
            for name in ROOT_FILES:
                if (self.root / name).exists(): shutil.copyfile(self.root / name, staging / name)
            seal(staging)
            staging.rename(failed)
            sync_directory(self.directory)
        backup.restore_snapshot(self.root, self.recovery / 'database.tar.gz')
        for name in ROOT_FILES:
            atomic_copy(self.recovery / 'root' / name, self.root / name)
        self.replace_web(self.recovery / 'root/web', 'failed-web')
        for name,destination in HOST_FILES.items():
            atomic_copy(self.recovery / 'host' / name, destination, 0o755 if name in ('node-agent','streamtool-worker-policy','update.sh') or name.endswith('.py') else 0o440 if name.endswith('sudoers') else 0o644)
        atomic_copy(self.root / 'node.env', Path('/etc/streamtool/node.env'))
        self.start()

    def private_json(self, address, path, name=None, token=None, basic=None):
        context = ssl.create_default_context(cafile=str(self.root / 'certs/ca.crt'))
        context.load_cert_chain(str(self.root / 'certs/controller.crt'), str(self.root / 'certs/controller.key'))
        host,port = address
        connection = http.client.HTTPSConnection(name or host, port, context=context, timeout=5)
        connection.sock = context.wrap_socket(socket.create_connection((host,port), timeout=5), server_hostname=name or host)
        try:
            headers = {'Authorization':'Bearer ' + token} if token else {}
            if basic: headers['Authorization'] = 'Basic ' + base64.b64encode(('controller:' + basic).encode()).decode()
            connection.request('GET',path,headers=headers)
            response = connection.getresponse()
            if response.status != 200:
                raise ValueError('private readiness unavailable')
            body = response.read((1 << 20) + 1)
            if len(body) > 1 << 20:
                raise ValueError('private readiness response too large')
            return json.loads(body)
        finally:
            connection.close()

    def health(self, job, old):
        expected = job['old'] if old else job['target']
        deadline = time.monotonic() + 90
        while True:
            try:
                current = self.installed()
                if current != expected:
                    raise ValueError('installed version/schema/image mismatch')
                api = self.private_json(('172.30.82.2',8080), '/v1/installation', token=(self.root / 'secrets/admin_token').read_text().strip())
                if api != {'version':expected['version'], 'schema':expected['schema'], 'maintenance_directory':'/maintenance'}:
                    raise ValueError('running API version/schema mismatch')
                agent = self.private_json(('172.30.80.1',8443), '/v1/heartbeat')
                if agent.get('maintenance_directory') != '/var/lib/streamtool-updater/admission' or agent['version'] != expected['version'] or agent['worker_image'] != expected['worker_image'] or agent['node_id'] != '00000000-0000-4000-8000-000000000001':
                    raise ValueError('Agent identity/version/image mismatch')
                self.private_json(('172.30.80.4',9997), '/v3/paths/list', name='edge', basic=(self.root / 'secrets/edge_control_token').read_text().strip())
                # Public TLS/cabinet is separate from private socket/job health.
                public = http.client.HTTPSConnection(settings(self.root / '.env')['DOMAIN'],443,timeout=5)
                try:
                    public.request('GET','/')
                    response = public.getresponse()
                    page = response.read((1 << 20)+1)
                    if response.status != 200 or len(page)>1<<20 or b'streamtool-relay' not in page:
                        raise ValueError('public cabinet unavailable')
                    public.request('GET','/v1/auth/setup')
                    response = public.getresponse()
                    data = response.read(4097)
                    if response.status != 200 or len(data)>4096 or not isinstance(json.loads(data).get('required'),bool):
                        raise ValueError('public control API unavailable')
                finally:
                    public.close()
                return
            except Exception:
                if time.monotonic() >= deadline:
                    raise ValueError('application readiness failed') from None
                time.sleep(2)
