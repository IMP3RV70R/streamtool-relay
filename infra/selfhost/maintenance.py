#!/usr/bin/env python3
"""Host-owned admission fence; lock and marker never belong to app backups."""
import contextlib
import fcntl
import json
import os
from pathlib import Path
import tempfile
import time

DIRECTORY = Path('/var/lib/streamtool-updater/admission')


def durable_json(path, value):
    descriptor, temporary = tempfile.mkstemp(prefix='.journal-', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'w') as output:
            json.dump(value, output, separators=(',', ':'))
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        sync_directory(path.parent)
    finally:
        Path(temporary).unlink(missing_ok=True)


def sync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def initialize(directory=DIRECTORY):
    directory.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
    directory.mkdir(exist_ok=True, mode=0o755)
    directory.parent.chmod(0o755)
    directory.chmod(0o755)
    lock = directory / 'admission.lock'
    descriptor = os.open(lock, os.O_CREAT | os.O_RDONLY | os.O_NOFOLLOW, 0o644)
    os.fchmod(descriptor, 0o644)
    os.close(descriptor)


class Fence:
    def __init__(self, directory=DIRECTORY, wait=0):
        self.directory = directory
        self.wait = wait

    @contextlib.contextmanager
    def hold(self, job):
        with (self.directory / 'admission.lock').open('rb') as lock:
            deadline = time.monotonic() + self.wait
            while True:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline: raise
                    time.sleep(0.1)
            marker = self.directory / 'active.json'
            if marker.exists() and json.loads(marker.read_text()) != {'job': job}:
                raise ValueError('another maintenance operation is active')
            durable_json(marker, {'job': job})
            marker.chmod(0o644)
            yield
            # Never clear on exception/process death. The coordinator releases only
            # after a durable commit and verified application health.

    def active(self, job):
        marker = self.directory / 'active.json'
        return marker.exists() and json.loads(marker.read_text()) == {'job': job}

    def release(self, job):
        marker = self.directory / 'active.json'
        if marker.exists():
            if not self.active(job):
                raise ValueError('maintenance ownership mismatch')
            marker.unlink()
            sync_directory(self.directory)


@contextlib.contextmanager
def operation(directory=DIRECTORY):
    """Serialize manual backup/restore and leaf renewal against host updates."""
    with (directory / 'admission.lock').open('rb') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if (directory / 'active.json').exists():
            raise ValueError('installation maintenance/recovery is active')
        yield
