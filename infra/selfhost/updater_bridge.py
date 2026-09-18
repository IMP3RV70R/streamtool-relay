"""Fixed local protocol and durable inbox; no client paths, URLs or commands."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import socket
import stat
import struct
import sys
import threading
import time
from types import SimpleNamespace

import updater
from maintenance import durable_json, sync_directory
from updater_engine import TERMINAL
from updater_host import LinuxHost, command

SOCKET = Path('/var/lib/streamtool-updater/socket/control.sock')
MAX_REQUEST = 4096
MAX_RESPONSE = 32768


class ProtocolError(Exception):
    def __init__(self, code):
        self.code = code


def digest(raw):
    if not isinstance(raw, str) or len(raw) != 64 or any(c not in '0123456789abcdef' for c in raw):
        raise ProtocolError('invalid_request')
    return raw


def job_identifier(raw):
    try:
        if not isinstance(raw, str): raise ValueError('invalid id')
        return updater.identifier(raw)
    except ValueError:
        raise ProtocolError('invalid_request')


def architecture():
    return {'x86_64':'amd64', 'aarch64':'arm64', 'arm64':'arm64'}[platform.machine()]


def bounded_json(path, maximum=65536):
    with path.open('rb') as incoming:
        data = incoming.read(maximum + 1)
    if len(data) > maximum:
        raise ValueError('local metadata too large')
    return json.loads(data)


class Bridge:
    def __init__(self, state=None):
        self.state = Path(state) if state is not None else updater.STATE

    def request_path(self, job_id):
        return self.state / 'requests' / (updater.identifier(job_id) + '.json')

    def requested(self):
        pointer = self.state / 'requested.json'
        return updater.identifier(bounded_json(pointer)['id']) if pointer.exists() else None

    def receipt(self, job_id):
        path = self.request_path(job_id)
        return bounded_json(path) if path.exists() else None

    def job(self, job_id):
        path = self.state / job_id / 'job.json'
        return updater.coordinator(job_id).load() if path.exists() else None

    def status(self, job_id=None):
        if not job_id:
            requested = self.requested()
            waiting = self.receipt(requested) if requested else None
            current = updater.current()
            latest = self.job(current) if current else None
            job_id = current if latest and (not waiting or latest.get('created',0)>waiting.get('created',waiting['deadline']-86400)) else requested or current
        if not job_id:
            return {'phase':'IDLE'}
        job_id = updater.identifier(job_id)
        receipt = self.receipt(job_id)
        job = self.job(job_id)
        if job:
            return {'id':job_id, 'digest':job['digest'], 'phase':job['phase'],
                    'installed_version':job['old']['version'] if job['phase'] == 'ROLLED_BACK' else '',
                    'target_version':job['target']['version'],
                    'error':job.get('error', ''),
                    'restored_credentials_revoked':job.get('restored_credentials_revoked', False)}
        if receipt:
            return {'id':job_id, 'digest':receipt['digest'], 'phase':receipt['phase'], 'error':receipt.get('error','')}
        raise ProtocolError('not_found')

    def candidate(self):
        pointer = self.state / 'candidate.json'
        if not pointer.exists():
            return None
        stamp = digest(bounded_json(pointer)['digest'])
        directory = self.state / 'catalog' / stamp
        data = (directory / 'manifest.json').read_bytes()
        if len(data) > 65536 or hashlib.sha256(data).hexdigest() != stamp:
            raise ValueError('catalog integrity failure')
        manifest = json.loads(data)
        old = LinuxHost(updater.ROOT, directory).installed()
        if manifest['sequence'] <= old['sequence']:
            return None
        # Recheck trust, compatibility, expiry and watermark before every offer/start.
        command(updater.VERIFIER, 'verify', '--metadata-only', '--trust', updater.TRUST,
                '--state', str(self.state / 'watermark.json'), '--manifest', str(directory / 'manifest.json'),
                '--signature', str(directory / 'manifest.sig'), '--architecture', architecture(),
                '--installed-sequence', str(old['sequence']), '--installed-schema', str(old['schema']), timeout=5)
        return {'digest':stamp, 'version':manifest['version'], 'notes':manifest.get('notes',''),
                'expires':manifest['expires']}

    def wake(self):
        command('systemctl', 'start', '--no-block', 'streamtool-updater.service', timeout=5)

    def start(self, job_id, stamp):
        job_id = job_identifier(job_id)
        stamp = digest(stamp)
        with updater.exclusive():
            existing = self.receipt(job_id)
            job = self.job(job_id)
            if existing or job:
                if (existing or job)['digest'] != stamp:
                    raise ProtocolError('conflict')
                result = self.status(job_id)
            else:
                for previous in (self.requested(), updater.current()):
                    if previous and self.status(previous)['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
                        raise ProtocolError('busy')
                # A crash may have saved a receipt before its current pointer.
                requests = self.state / 'requests'
                if requests.exists():
                    receipts = list(requests.glob('*.json'))
                    if len(receipts) >= 4096:
                        raise ProtocolError('unavailable')
                    for path in receipts:
                        if self.status(path.stem)['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
                            raise ProtocolError('busy')
                candidate = self.candidate()
                if not candidate or candidate['digest'] != stamp:
                    raise ProtocolError('target_unavailable')
                requests.mkdir(mode=0o700, exist_ok=True)
                durable_json(self.request_path(job_id), {'id':job_id, 'digest':stamp,
                             'phase':'REQUESTED', 'created':time.time(), 'deadline':time.time()+86400})
                result = self.status(job_id)
            # Publish pointer after the receipt. Exact retry repairs interrupted publication.
            if result['phase'] == 'REQUESTED':
                previous = self.requested()
                if previous and previous != job_id and self.status(previous)['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
                    raise ProtocolError('busy')
                durable_json(self.state / 'requested.json', {'id':job_id})
            if result['phase'] in {'REQUESTED','QUEUED'}:
                receipt = self.receipt(job_id)
                if receipt is not None:
                    attempts = receipt.get('wake_attempts',0)
                    if attempts >= 8:
                        job = self.job(job_id)
                        if job is not None:
                            # QUEUED may already own a persistent maintenance
                            # marker after process death. Only the engine may
                            # make the verified decision to reopen admission.
                            raise ProtocolError('unavailable')
                        receipt.update(phase='FAILED',error='coordinator_start_failed')
                        durable_json(self.request_path(job_id),receipt)
                        return self.status(job_id)
                    receipt['wake_attempts'] = attempts+1
                    durable_json(self.request_path(job_id),receipt)
        if result['phase'] in {'REQUESTED','QUEUED'}:
            try:
                self.wake()
            except Exception:
                # API keeps its outbox pending and retries the same receipt. Wake
                # attempts are durable/bounded; boot also resumes accepted work.
                raise ProtocolError('unavailable')
        return result

    def resume_requested(self):
        """Called under the root coordinator lock, before the existing engine resumes."""
        job_id = self.requested()
        if not job_id:
            return None
        receipt = self.receipt(job_id)
        job = self.job(job_id)
        if job:
            # Historical owner receipts remain durable after completion. They
            # must not hide a newer administrative job at boot. Recovery that
            # still requires intervention is deliberately not completed work.
            if job['phase'] in TERMINAL - {'RECOVERY_REQUIRED'}:
                return None
            current = updater.current()
            if current and current != job_id:
                other = self.job(current)
                if other and other['phase'] not in TERMINAL - {'RECOVERY_REQUIRED'}:
                    raise ValueError('conflicting active update journals')
            return job_id
        if receipt['phase'] != 'REQUESTED':
            return None
        try:
            if time.time() > receipt['deadline']:
                raise ValueError('request expired')
            candidate = self.candidate()
            if not candidate or candidate['digest'] != receipt['digest']:
                raise ValueError('target no longer available')
            directory = self.state / 'catalog' / receipt['digest']
            updater.queue(SimpleNamespace(id=job_id, manifest=directory/'manifest.json',
                          signature=directory/'manifest.sig', bundle=directory/'bundle.tar.gz', architecture=architecture()))
        except Exception:
            if self.job(job_id):
                raise  # A durable job exists: resume it, never erase an uncertain decision.
            receipt.update(phase='FAILED', error='queue_failed')
            durable_json(self.request_path(job_id), receipt)
            return None
        return job_id

    def handle(self, request):
        if not isinstance(request, dict) or type(request.get('protocol')) is not int or request['protocol'] != 1:
            raise ProtocolError('invalid_request')
        op = request.get('op')
        if op == 'catalog' and set(request) == {'protocol','op'}:
            return {'release':self.candidate()}
        if op == 'status' and set(request) in ({'protocol','op'}, {'protocol','op','id'}):
            return self.status(job_identifier(request['id']) if 'id' in request else None)
        if op == 'start' and set(request) == {'protocol','op','id','digest'}:
            return self.start(request['id'], request['digest'])
        raise ProtocolError('invalid_request')


def strict_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ProtocolError('invalid_request')
        result[key] = value
    return result


class Server:
    def __init__(self, listener, bridge, allowed_uid=65532):
        self.listener, self.bridge, self.allowed_uid = listener, bridge, allowed_uid
        self.slots = threading.BoundedSemaphore(4)

    def serve_connection(self, connection):
        with connection:
            connection.settimeout(8)
            try:
                _, uid, _ = struct.unpack('3i', connection.getsockopt(socket.SOL_SOCKET, socket.SO_PEERCRED, 12))
                if uid != self.allowed_uid:
                    raise ProtocolError('unauthorized')
                with connection.makefile('rb') as incoming:
                    data = incoming.readline(MAX_REQUEST + 1)
                    if not data.endswith(b'\n') or len(data) > MAX_REQUEST:
                        raise ProtocolError('invalid_request')
                    request = json.loads(data, object_pairs_hook=strict_object)
                    result = {'protocol':1, 'ok':True, **self.bridge.handle(request)}
            except ProtocolError as error:
                result = {'protocol':1, 'ok':False, 'error':error.code}
            except BlockingIOError:
                result = {'protocol':1, 'ok':False, 'error':'busy'}
            except Exception:
                result = {'protocol':1, 'ok':False, 'error':'unavailable'}
            data = json.dumps(result, separators=(',',':')).encode() + b'\n'
            if len(data) > MAX_RESPONSE:
                data = b'{"protocol":1,"ok":false,"error":"unavailable"}\n'
            with contextlib.suppress(OSError):
                connection.sendall(data)

    def worker(self, connection):
        try:
            self.serve_connection(connection)
        finally:
            self.slots.release()

    def run(self):
        while True:
            connection, _ = self.listener.accept()
            if not self.slots.acquire(blocking=False):
                connection.close()
                continue
            threading.Thread(target=self.worker, args=(connection,), daemon=True).start()


def offer(arguments):
    """Administrative offline catalog preparation. Never reachable over the socket."""
    with arguments.manifest.open('rb') as incoming:
        data = incoming.read(65537)
    if not data or len(data) > 65536:
        raise ValueError('manifest too large')
    stamp = hashlib.sha256(data).hexdigest()
    catalog = updater.STATE / 'catalog'
    catalog.mkdir(mode=0o700, exist_ok=True)
    directory = catalog / stamp
    if directory.exists():
        raise ValueError('catalog release already exists')
    directory.mkdir(mode=0o700)
    try:
        (directory/'manifest.json').write_bytes(data)
        with arguments.signature.open('rb') as incoming:
            signature = incoming.read(257)
        if len(signature) > 256:
            raise ValueError('signature too large')
        (directory/'manifest.sig').write_bytes(signature)
        with arguments.bundle.open('rb') as incoming, (directory/'bundle.tar.gz').open('xb') as output:
            total = 0
            while chunk := incoming.read(1 << 20):
                total += len(chunk)
                if total > 8 << 30:
                    raise ValueError('bundle too large')
                output.write(chunk)
            output.flush(); os.fsync(output.fileno())
        old = LinuxHost(updater.ROOT, directory).installed()
        command(updater.VERIFIER, 'verify', '--trust', updater.TRUST, '--state', str(updater.STATE/'watermark.json'),
                '--manifest', str(directory/'manifest.json'), '--signature', str(directory/'manifest.sig'),
                '--bundle', str(directory/'bundle.tar.gz'), '--destination', str(directory/'stage'),
                '--architecture', architecture(), '--installed-sequence', str(old['sequence']), '--installed-schema', str(old['schema']), timeout=900)
        for path in (directory/'manifest.json', directory/'manifest.sig'):
            with path.open('rb') as incoming: os.fsync(incoming.fileno())
        sync_directory(directory); sync_directory(catalog)
        durable_json(updater.STATE/'candidate.json', {'digest':stamp})
    except BaseException:
        shutil.rmtree(directory)
        raise


def main():
    if sys.platform != 'linux' or os.geteuid() != 0:
        raise ValueError('bridge requires Linux root')
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest='action', required=True)
    commands.add_parser('serve')
    prepare = commands.add_parser('offer')
    for name in ('manifest','signature','bundle'):
        prepare.add_argument('--'+name, type=Path, required=True)
    arguments = parser.parse_args()
    if arguments.action == 'offer':
        with updater.exclusive(): offer(arguments)
        return
    os.umask(0o077)
    descriptor = os.open(updater.STATE/'bridge.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
    SOCKET.parent.mkdir(mode=0o750, exist_ok=True)
    os.chown(SOCKET.parent, 0, 65532); SOCKET.parent.chmod(0o750)
    if SOCKET.exists():
        if not stat.S_ISSOCK(SOCKET.lstat().st_mode):
            raise ValueError('unexpected socket path')
        SOCKET.unlink()
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as listener:
        listener.bind(str(SOCKET))
        os.chown(SOCKET, 0, 65532); SOCKET.chmod(0o660)
        listener.listen(8)
        Server(listener, Bridge()).run()


if __name__ == '__main__':
    try: main()
    except Exception:
        print('Update bridge unavailable; inspect service state over SSH.', file=sys.stderr)
        sys.exit(1)
