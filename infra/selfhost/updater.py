#!/usr/bin/env python3
"""Root-only offline queue/resume/status for the independent host coordinator."""
import argparse
import errno
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import sys
import time
import uuid

from maintenance import DIRECTORY, Fence, durable_json
from updater_engine import Coordinator, TERMINAL
from updater_host import DiskReserve, LinuxHost, command

STATE = Path('/var/lib/streamtool-updater/journal')
ROOT = Path('/opt/streamtool')
VERIFIER = '/usr/local/libexec/streamtool-updater/release-tool'
TRUST = '/etc/streamtool/release-trust.json'


@contextlib.contextmanager
def exclusive():
    descriptor = os.open(STATE / 'coordinator.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(descriptor)


def identifier(raw):
    canonical = str(uuid.UUID(raw))
    if canonical != raw:
        raise ValueError('job id must be canonical UUID')
    return canonical


def current():
    pointer = STATE / 'current.json'
    if not pointer.exists():
        return None
    return identifier(json.loads(pointer.read_text())['job'])


def coordinator(job):
    directory = STATE / job
    return Coordinator(directory, Fence(DIRECTORY, wait=55), LinuxHost(ROOT, directory))


def queue(arguments):
    job_id = identifier(arguments.id)
    with arguments.manifest.open('rb') as incoming:
        data = incoming.read(65537)
    if not data or len(data) > 65536:
        raise ValueError('invalid manifest size')
    digest = hashlib.sha256(data).hexdigest()
    directory = STATE / job_id
    if directory.exists():
        existing = coordinator(job_id).load()
        if existing['digest'] != digest:
            raise ValueError('job id already belongs to another release')
        if existing['phase'] == 'QUEUED' and current() != job_id:
            previous = current()
            if previous and coordinator(previous).load()['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
                raise ValueError('another update is active')
            durable_json(STATE / 'current.json', {'job':job_id})
        return existing
    previous = current()
    if previous and coordinator(previous).load()['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
        raise ValueError('another update is active or recovery is required')
    directory.mkdir(mode=0o700)
    directory.chmod(0o700)
    try:
        host = LinuxHost(ROOT, directory)
        old = host.installed()
        # Snapshot manifest/signature into private job storage before verification,
        # so parsed job metadata cannot race a change of administrative input files.
        manifest_path = directory / 'manifest.json'
        manifest_path.write_bytes(data)
        with arguments.signature.open('rb') as incoming:
            signature = incoming.read(257)
        if len(signature) > 256: raise ValueError('signature too large')
        signature_path = directory / 'manifest.sig'
        signature_path.write_bytes(signature)
        command(VERIFIER, 'verify', '--trust', TRUST, '--state', str(STATE / 'watermark.json'), '--manifest', str(manifest_path), '--signature', str(signature_path), '--bundle', str(arguments.bundle), '--destination', str(directory / 'stage'), '--architecture', arguments.architecture, '--installed-sequence', str(old['sequence']), '--installed-schema', str(old['schema']), timeout=900)
        manifest = json.loads(data)
        target = {'version':manifest['version'], 'sequence':manifest['sequence'], 'schema':manifest['target_schema'], 'worker_image':(directory / 'stage/worker-image').read_text().strip()}
        job = {'id':job_id, 'digest':digest, 'phase':'QUEUED', 'old':old, 'target':target, 'rollback_resumes':0, 'recovery_deadline':time.time()+86400, 'created':time.time(), 'updated':time.time()}
        durable_json(directory / 'job.json', job)
        durable_json(STATE / 'current.json', {'job':job_id})
        return job
    except BaseException:
        if not (directory / 'job.json').exists():
            __import__('shutil').rmtree(directory)
        raise


def resume_job(job_id):
    reserve = DiskReserve(STATE / job_id)
    # Covers a reboot after ENOSPC as well as failures in subprocesses that
    # cannot communicate an errno. Never reallocate during recovery.
    reserve.release()
    try:
        job = coordinator(job_id).resume()
    except OSError as error:
        if error.errno != errno.ENOSPC or not reserve.release(force=True):
            raise
        # Exactly one retry from the durable phase. Installation is never
        # replayed after partial replacement; the engine chooses matching rollback.
        job = coordinator(job_id).resume()
    if job['phase'] in TERMINAL - {'RECOVERY_REQUIRED'}:
        reserve.release(force=True)
    return job


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest='action', required=True)
    commands.add_parser('resume')
    commands.add_parser('status')
    commands.add_parser('release-space')
    start = commands.add_parser('queue')
    start.add_argument('--id', required=True)
    start.add_argument('--manifest', type=Path, required=True)
    start.add_argument('--signature', type=Path, required=True)
    start.add_argument('--bundle', type=Path, required=True)
    start.add_argument('--architecture', choices=('amd64','arm64'), required=True)
    arguments = parser.parse_args()
    if os.geteuid() != 0 or sys.platform != 'linux':
        raise ValueError('coordinator requires Linux root')
    if arguments.action == 'status':
        from updater_bridge import Bridge
        print(json.dumps(Bridge().status()))
        return
    else:
        with exclusive():
            if arguments.action == 'release-space':
                from updater_bridge import Bridge
                for job_id in {current(), Bridge().requested()} - {None}:
                    DiskReserve(STATE / job_id).release()
                print(json.dumps({'phase':'SPACE_CHECKED'}))
                return
            if arguments.action == 'queue':
                from updater_bridge import Bridge
                bridge = Bridge()
                pending = bridge.requested()
                if pending and pending != arguments.id and bridge.status(pending)['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
                    raise ValueError('an owner update request is active')
                job = queue(arguments)
            else:
                from updater_bridge import Bridge
                job_id = Bridge().resume_requested() or current()
                job = resume_job(job_id) if job_id else None
        if arguments.action == 'queue' and job['phase'] == 'QUEUED':
            command('systemctl', 'start', '--no-block', 'streamtool-updater.service')
    print(json.dumps({key:job[key] for key in ('id','phase','old','target','error','rollback_resumes','restored_credentials_revoked') if key in job} if job else {'phase':'IDLE'}))


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('Host update unavailable; inspect private journal and service state over SSH.', file=sys.stderr)
        sys.exit(1)
