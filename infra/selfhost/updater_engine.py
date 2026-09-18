#!/usr/bin/env python3
"""Durable update transaction. Driver operations are fixed, trusted host actions."""
import json
from pathlib import Path
import time

from maintenance import durable_json

TERMINAL = {'SUCCEEDED', 'ROLLED_BACK', 'FAILED', 'RECOVERY_REQUIRED'}
PHASES = TERMINAL | {'QUEUED', 'PREPARING', 'INSTALLING', 'VERIFYING', 'ROLLING_BACK', 'ROLLBACK_VERIFYING', 'COMMITTED', 'RECOVERED', 'ABORTING', 'ABORTED'}


class Coordinator:
    def __init__(self, directory, fence, driver, clock=time.time):
        self.directory = Path(directory)
        self.fence = fence
        self.driver = driver
        self.clock = clock
        self.path = self.directory / 'job.json'

    def load(self):
        if self.path.stat().st_size > 16384:
            raise ValueError('invalid update journal')
        job = json.loads(self.path.read_text())
        if job['phase'] not in PHASES or job['id'] != self.directory.name or job['rollback_resumes'] not in range(9):
            raise ValueError('invalid update journal')
        return job

    def phase(self, job, phase, error=None):
        job['phase'] = phase
        if error:
            # Never persist exception strings: subprocess diagnostics can hold keys.
            job['error'] = error
        job['updated'] = self.clock()
        durable_json(self.path, job)

    def finish(self, job, committed, terminal):
        # Commit before opening admission. A crash after release must never cause
        # snapshot rollback of authentication/configuration written afterwards.
        self.phase(job, committed)
        self.fence.release(job['id'])
        self.phase(job, terminal)

    def rollback(self, job):
        if job['rollback_resumes'] >= 8 or self.clock() > job['recovery_deadline']:
            self.phase(job, 'RECOVERY_REQUIRED', 'rollback_budget_exhausted')
            return
        job['rollback_resumes'] += 1
        self.phase(job, 'ROLLING_BACK')
        try:
            self.driver.restore(job)
            self.phase(job, 'ROLLBACK_VERIFYING')
            self.driver.health(job, old=True)
            job['restored_credentials_revoked'] = True
            self.finish(job, 'RECOVERED', 'ROLLED_BACK')
        except Exception:
            self.phase(job, 'RECOVERY_REQUIRED', 'rollback_failed')

    def abort(self, job):
        self.phase(job, 'ABORTING')
        try:
            self.driver.abort(job)
            self.driver.health(job, old=True)
            self.finish(job, 'ABORTED', 'FAILED')
        except Exception:
            self.phase(job, 'RECOVERY_REQUIRED', 'preparation_recovery_failed')

    def resume(self):
        job = self.load()
        phase = job['phase']
        if phase in TERMINAL:
            return job
        # Commit proves health while admission was closed. If admission is already
        # open, do not rewind any new state, even after a reboot.
        if phase in {'COMMITTED', 'RECOVERED', 'ABORTED'} and not self.fence.active(job['id']):
            self.phase(job, {'COMMITTED':'SUCCEEDED','RECOVERED':'ROLLED_BACK','ABORTED':'FAILED'}[phase])
            return job
        with self.fence.hold(job['id']):
            if phase in {'COMMITTED', 'RECOVERED', 'ABORTED'}:
                try:
                    self.driver.health(job, old=phase != 'COMMITTED')
                    self.finish(job, phase, {'COMMITTED':'SUCCEEDED','RECOVERED':'ROLLED_BACK','ABORTED':'FAILED'}[phase])
                except Exception:
                    self.phase(job, 'RECOVERY_REQUIRED', 'committed_health_failed')
            elif phase in {'INSTALLING', 'VERIFYING', 'ROLLING_BACK', 'ROLLBACK_VERIFYING'}:
                self.rollback(job)
            elif phase in {'PREPARING', 'ABORTING'}:
                # Preparation might have stopped services. Never blindly restart
                # installation after an uncertain process/host interruption.
                self.abort(job)
            else:
                if self.clock() > job['recovery_deadline']:
                    self.phase(job, 'ABORTED', 'job_expired')
                    self.fence.release(job['id'])
                    self.phase(job, 'FAILED')
                    return job
                try:
                    self.driver.idle(job)
                except Exception:
                    self.phase(job, 'ABORTED', 'host_not_idle')
                    self.fence.release(job['id'])
                    self.phase(job, 'FAILED')
                    return job
                self.phase(job, 'PREPARING')
                try:
                    self.driver.prepare(job)
                except Exception:
                    self.phase(job, 'ABORTING', 'preparation_failed')
                    self.abort(job)
                    return job
                self.phase(job, 'INSTALLING')
                try:
                    self.driver.install(job)
                    self.phase(job, 'VERIFYING')
                    self.driver.health(job, old=False)
                except Exception:
                    self.phase(job, 'ROLLING_BACK', 'installation_failed')
                    self.rollback(job)
                else:
                    self.finish(job, 'COMMITTED', 'SUCCEEDED')
        return job
