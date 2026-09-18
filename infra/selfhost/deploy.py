#!/usr/bin/env python3
"""Idempotent initial-deployment operations for an already verified release.

The autonomous installer owns admission/journaling. This is never an updater and
never removes another installation, workload, configuration or owner database.
"""
import ctypes
import hashlib
import tarfile
import importlib.util
import json
import os
import platform
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile


def module(stage, name):
    spec = importlib.util.spec_from_file_location(name, stage / (name + '.py'))
    result = importlib.util.module_from_spec(spec)
    # Executing reviewed source must not add __pycache__ to signed staging.
    exec(compile((stage / (name + '.py')).read_bytes(), str(stage / (name + '.py')), 'exec'), result.__dict__)
    return result


def sync(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)


def durable_tree(root):
    for path in root.rglob('*'):
        if path.is_symlink(): raise ValueError('unsafe deployment tree')
        if path.is_file():
            with path.open('rb') as file: os.fsync(file.fileno())
    for path in sorted((p for p in root.rglob('*') if p.is_dir()), reverse=True): sync(path)
    sync(root)


def publish(source, destination):
    if sys.platform == 'linux':
        libc = ctypes.CDLL(None,use_errno=True)
        rename = libc.renameat2
        rename.argtypes = [ctypes.c_int,ctypes.c_char_p,ctypes.c_int,ctypes.c_char_p,ctypes.c_uint]
        rename.restype = ctypes.c_int
        if rename(-100,os.fsencode(source),-100,os.fsencode(destination),1) != 0:
            error = ctypes.get_errno()
            raise OSError(error,os.strerror(error),str(destination))
    else:
        # Local bootstrap fixtures only; the autonomous entry point requires Linux.
        if destination.exists() or destination.is_symlink(): raise FileExistsError(destination)
        os.rename(source,destination)


def run(*args, timeout=180):
    subprocess.run(args, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=timeout)


def manifest_ids(archive, expected):
    """Resolve only content-addressed OCI manifests present in signed image bytes."""
    result = {}
    with tarfile.open(archive, 'r') as saved:
        def read(name):
            member = saved.getmember(name)
            if not member.isfile() or member.size > 65536: raise ValueError('invalid image metadata')
            return saved.extractfile(member).read()
        try: index = json.loads(read('index.json'))
        except KeyError: return result  # Classic Docker archives expose config IDs.
        descriptors = index.get('manifests', [])
        if not isinstance(descriptors, list) or len(descriptors) > 16: raise ValueError('invalid image index')
        for descriptor in descriptors:
            stamp = descriptor.get('digest', '')
            if not re.fullmatch(r'sha256:[0-9a-f]{64}', stamp): raise ValueError('invalid image digest')
            data = read('blobs/sha256/' + stamp[7:])
            if hashlib.sha256(data).hexdigest() != stamp[7:]: raise ValueError('invalid image digest')
            manifest = json.loads(data)
            config = manifest.get('config', {}).get('digest')
            for name, wanted in expected.items():
                if config == wanted:
                    if name in result: raise ValueError('ambiguous image manifest')
                    result[name] = stamp
    return result


class InitialDeployment:
    def __init__(self, root, stage, job, execute=run):
        self.root, self.stage, self.job, self.execute = root, stage, job, execute
        self.bootstrap = module(stage, 'bootstrap')
        self.address, _ = self.bootstrap.public_address(job['address'])
        self.images = {name: (stage / (name + '-image')).read_text().strip() for name in ('api','worker','proxy','edge')}
        if any(not re.fullmatch(r'sha256:[0-9a-f]{64}', image) for image in self.images.values()): raise ValueError('invalid image')
        self.declared_images = dict(self.images)
        resolved = job.get('runtime_images')
        if resolved is not None:
            candidates = manifest_ids(stage / 'images.tar', self.declared_images)
            if not isinstance(resolved, dict) or set(resolved) != set(self.images) or any(not isinstance(resolved[name], str) or resolved[name] not in (self.images[name], candidates.get(name)) for name in self.images):
                raise ValueError('invalid runtime image identity')
            self.images = dict(resolved)

    def owned(self):
        marker = self.root / 'INSTALLATION_JOB'
        if self.root.is_symlink() or marker.is_symlink() or marker.read_text().strip() != self.job['job_id']:
            raise ValueError('foreign installation')

    def image_load(self):
        self.execute('docker', 'load', '-i', str(self.stage / 'images.tar'), timeout=900)
        architecture = {'x86_64':'amd64','aarch64':'arm64'}[platform.machine()]
        candidates = manifest_ids(self.stage / 'images.tar', self.declared_images)
        resolved = {}
        for name, declared in self.declared_images.items():
            references = list(dict.fromkeys([declared] + ([candidates[name]] if name in candidates else [])))
            for image in references:
                try:
                    actual = subprocess.check_output(['docker','image','inspect','--format','{{.Os}}/{{.Architecture}}',image],
                                                     text=True,stderr=subprocess.DEVNULL,timeout=30).strip()
                except subprocess.CalledProcessError:
                    if image == references[-1]: raise
                    continue
                if actual != 'linux/' + architecture: raise ValueError('image platform mismatch')
                resolved[name] = image
                break
        self.images = resolved
        self.job['runtime_images'] = dict(resolved)

    def configure(self):
        if self.root.exists() or self.root.is_symlink():
            self.owned(); return  # Never rerun bootstrap against a committed configuration.
        staging = self.root.parent / ('.streamtool-bootstrap-' + self.job['job_id'])
        marker = staging / 'INSTALLATION_JOB'
        if staging.exists() or staging.is_symlink():
            if staging.is_symlink() or marker.is_symlink() or marker.read_text().strip() != self.job['job_id']:
                raise ValueError('foreign bootstrap stage')
        else:
            temporary = Path(tempfile.mkdtemp(prefix=staging.name + '-pending-',dir=self.root.parent))
            pending_marker = temporary / 'INSTALLATION_JOB'
            pending_marker.write_text(self.job['job_id'] + '\n'); pending_marker.chmod(0o600)
            durable_tree(temporary)
            # Publish marker and directory together. A crash before this operation
            # leaves only an unused private temporary directory, never an ambiguous
            # canonical stage that recovery would have to claim or erase.
            publish(temporary,staging); sync(staging.parent)
        self.bootstrap.initialize(staging, self.job['address'], *[self.images[n] for n in ('api','worker','proxy','edge')],
                                  resume=True, runtime_directory=self.root)
        (staging / 'VERSION').write_text(self.job['version'] + '\n')
        (staging / 'RELEASE_SEQUENCE').write_text(str(self.job['sequence']) + '\n')
        shutil.copytree(self.stage / 'web', staging / 'web', dirs_exist_ok=True)
        for path in (staging / 'web').rglob('*'): path.chmod(0o755 if path.is_dir() else 0o644)
        (staging / 'web').chmod(0o755)
        durable_tree(staging)
        publish(staging, self.root); sync(self.root.parent)
        self.owned()

    def copy(self, source, target, mode):
        if target.is_symlink(): raise ValueError('foreign destination')
        target.parent.mkdir(parents=True, exist_ok=True)
        if target.exists():
            if not target.is_file() or target.read_bytes() != source.read_bytes(): raise ValueError('foreign destination')
        else:
            temporary = target.with_name('.' + target.name + '.install-' + self.job['job_id'])
            if temporary.is_symlink(): raise ValueError('unsafe temporary file')
            with source.open('rb') as incoming, temporary.open('wb') as output:
                os.fchmod(output.fileno(), mode); shutil.copyfileobj(incoming, output)
                output.flush(); os.fsync(output.fileno())
            os.replace(temporary, target); sync(target.parent)
        target.chmod(mode)

    @staticmethod
    def destinations():
        result = {}
        for name in ('updater.py','updater_bridge.py','updater_engine.py','updater_host.py','maintenance.py','backup.py','release-tool'):
            result[name] = (Path('/usr/local/libexec/streamtool-updater') / name, 0o755)
        for name in ('streamtool-updater.service','streamtool-updater-space.service','streamtool-updater-bridge.service',
                     'streamtool-agent.service','streamtool-certificates.service','streamtool-certificates.timer'):
            result[name] = (Path('/etc/systemd/system') / name, 0o644)
        result['node-agent'] = (Path('/usr/local/bin/node-agent'), 0o755)
        result['streamtool-worker-policy'] = (Path('/usr/local/libexec/streamtool-worker-policy'), 0o755)
        result['streamtool-agent.sudoers'] = (Path('/etc/sudoers.d/streamtool-agent'), 0o440)
        return result

    def host(self):
        self.owned()
        # The preflight reserved this identity before any deployment mutation.
        found = subprocess.run(['getent','passwd','streamtool-agent'], stdout=subprocess.DEVNULL)
        if found.returncode != 0:
            self.execute('useradd','--system','--user-group','--no-create-home','--shell','/usr/sbin/nologin','streamtool-agent')
        self.execute('usermod','-a','-G','docker','streamtool-agent')
        sys.path.insert(0, str(self.stage))
        import maintenance, backup
        maintenance.initialize()
        updater = Path('/var/lib/streamtool-updater')
        (updater / 'journal').mkdir(mode=0o700, parents=True, exist_ok=True)
        socket = updater / 'socket'; socket.mkdir(mode=0o750, exist_ok=True); os.chown(socket, 0, 65532); socket.chmod(0o750)
        for name, (target, mode) in self.destinations().items(): self.copy(self.stage / name, target, mode)
        for name in ('backup.py','renew.py','maintenance.py','update.sh'):
            self.copy(self.stage / name, Path('/usr/local/libexec/streamtool-maintenance') / name, 0o755)
        self.execute('install','-d','-m','0750','-o','root','-g','streamtool-agent','/etc/streamtool')
        for name in ('node.env','worker-network.env'): self.copy(self.root / name, Path('/etc/streamtool') / name, 0o600)
        self.copy(Path(self.job['directory']) / 'trust.json', Path('/etc/streamtool/release-trust.json'), 0o600)
        self.execute('visudo','-cf','/etc/sudoers.d/streamtool-agent')
        backup.normalize(self.root)
        durable_tree(self.root)

    def start(self):
        self.owned()
        from installer_application import network_check
        network_check(owned=True)
        self.execute('systemctl','daemon-reload')
        self.execute('docker','compose','--project-directory',str(self.root),'-f',str(self.root / 'compose.yml'),'up','-d','--pull','never', timeout=180)
        self.execute('systemctl','enable','--now','streamtool-agent.service')
        self.execute('systemctl','enable','streamtool-updater.service','streamtool-updater-space.service')
        self.execute('systemctl','enable','--now','streamtool-updater-bridge.service','streamtool-certificates.timer')

    def ready(self):
        self.owned()
        sys.path.insert(0, str(self.stage))
        from updater_host import LinuxHost
        target = {'version':self.job['version'], 'sequence':self.job['sequence'],
                  'schema':self.job['schema'], 'worker_image':self.images['worker']}
        LinuxHost(self.root, Path(self.job['directory'])).health({'target':target}, old=False)
