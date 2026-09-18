"""Real file/SQLite/process-interruption checks of the durable coordinator.
The fixture driver does not establish Linux Docker/systemd/public-TLS acceptance.
"""
import contextlib
import fcntl
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import time
import unittest
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'infra/selfhost'))
from maintenance import Fence, durable_json, initialize
from updater_engine import Coordinator
from backup import revoke_restored_auth


class FileHost:
    def __init__(self, directory, fail='', crash=''):
        self.directory = Path(directory)
        self.root = self.directory / 'application'
        self.fail = fail
        self.crash = crash

    def event(self, event):
        if self.crash == event:
            os._exit(93)
        if self.fail == event:
            raise ValueError('private-fixture-secret must never appear in journal')

    def idle(self, job):
        self.event('idle')

    def prepare(self, job):
        self.event('before_backup')
        backup = self.directory / 'snapshot'
        shutil.copytree(self.root, backup)
        self.event('after_backup')

    def install(self, job):
        self.event('before_install')
        with contextlib.closing(sqlite3.connect(self.root / 'database')) as database, database:
            database.execute('CREATE TABLE new_schema(value TEXT)')
            database.execute("INSERT INTO data VALUES('target-version-data')")
        self.event('after_migration')
        (self.root / 'VERSION').write_text('new')
        self.event('after_install')

    def restore(self, job):
        self.event('before_restore')
        # Deliberately reapply the immutable snapshot on every interrupted resume.
        shutil.copyfile(self.directory / 'snapshot/database', self.root / 'database')
        self.event('after_database_restore')
        shutil.copyfile(self.directory / 'snapshot/VERSION', self.root / 'VERSION')
        self.event('after_restore')
        with contextlib.closing(sqlite3.connect(self.root / 'database')) as database, database:
            revoke_restored_auth(database)

    def abort(self, job):
        self.event('abort')

    def health(self, job, old):
        self.event('old_health' if old else 'new_health')
        if (self.root / 'VERSION').read_text() != ('old' if old else 'new'):
            raise ValueError('mixed application version')
        with contextlib.closing(sqlite3.connect(self.root / 'database')) as database, database:
            schema = bool(database.execute("SELECT count(*) FROM sqlite_master WHERE name='new_schema'").fetchone()[0])
            if schema == old:
                raise ValueError('old application paired with new schema')


class UpdateTransactionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.directory = Path(self.temporary.name) / str(uuid.uuid4())
        self.directory.mkdir(mode=0o700)
        self.admission = Path(self.temporary.name) / 'admission'
        initialize(self.admission)
        root = self.directory / 'application'
        root.mkdir()
        (root / 'VERSION').write_text('old')
        (root / 'encryption-key').write_bytes(os.urandom(32))
        self.original_key = (root / 'encryption-key').read_bytes()
        with contextlib.closing(sqlite3.connect(root / 'database')) as database, database:
            database.executescript("CREATE TABLE data(value TEXT); INSERT INTO data VALUES('owner/output configuration'); CREATE TABLE user_sessions(token TEXT); INSERT INTO user_sessions VALUES('consumed-session'); CREATE TABLE owner_recovery(hash TEXT); INSERT INTO owner_recovery VALUES('consumed-code'); CREATE TABLE owner_mfa(secret BLOB,last_step INTEGER); INSERT INTO owner_mfa VALUES(x'010203',1);")
        self.job = {'id':self.directory.name, 'phase':'QUEUED', 'rollback_resumes':0, 'recovery_deadline':time.time()+3600}
        durable_json(self.directory / 'job.json', self.job)
        self.fence = Fence(self.admission)

    def tearDown(self):
        self.temporary.cleanup()

    def engine(self, fail=''):
        return Coordinator(self.directory, self.fence, FileHost(self.directory, fail))

    def restored(self):
        self.assertEqual((self.directory / 'application/VERSION').read_text(), 'old')
        self.assertEqual((self.directory / 'application/encryption-key').read_bytes(), self.original_key)
        with contextlib.closing(sqlite3.connect(self.directory / 'application/database')) as database, database:
            self.assertEqual(database.execute('SELECT * FROM data').fetchall(), [('owner/output configuration',)])
            self.assertEqual(database.execute("SELECT count(*) FROM sqlite_master WHERE name='new_schema'").fetchone()[0], 0)
            self.assertEqual(database.execute('SELECT count(*) FROM user_sessions').fetchone()[0], 0)
            self.assertEqual(database.execute('SELECT count(*) FROM owner_recovery').fetchone()[0], 0)
            self.assertEqual(database.execute('SELECT secret FROM owner_mfa').fetchone()[0], bytes.fromhex('010203'))

    def crash(self, event, phase=None):
        if phase:
            self.job['phase'] = phase
            durable_json(self.directory / 'job.json', self.job)
        child = subprocess.run([sys.executable, __file__, '--crash', str(self.directory), str(self.admission), event], capture_output=True)
        self.assertEqual(child.returncode, 93)
        self.assertTrue(self.fence.active(self.job['id']))

    def test_success_and_idempotent_resume(self):
        self.assertEqual(self.engine().resume()['phase'], 'SUCCEEDED')
        with contextlib.closing(sqlite3.connect(self.directory / 'application/database')) as database, database:
            database.execute("INSERT INTO data VALUES('new owner mutation after commit')")
        self.assertEqual(self.engine('before_install').resume()['phase'], 'SUCCEEDED')
        self.assertFalse(self.fence.active(self.job['id']))
        self.assertEqual((self.directory / 'application/VERSION').read_text(), 'new')

    def test_expired_queued_job_never_prepares_or_installs(self):
        self.job['recovery_deadline']=time.time()-1
        durable_json(self.directory/'job.json',self.job)
        result=self.engine('idle').resume()
        self.assertEqual(result['phase'],'FAILED')
        self.assertEqual(result['error'],'job_expired')
        self.assertFalse((self.directory/'snapshot').exists())
        self.assertEqual((self.directory/'application/VERSION').read_text(),'old')
        self.assertFalse(self.fence.active(self.job['id']))

    def test_health_failure_restores_matching_schema_and_revokes_credentials(self):
        self.assertEqual(self.engine('new_health').resume()['phase'], 'ROLLED_BACK')
        self.restored()
        self.assertFalse(self.fence.active(self.job['id']))
        self.assertNotIn('private-fixture-secret', (self.directory / 'job.json').read_text())

    def test_preparation_failure_and_idle_refusal_do_not_change_database(self):
        for event in ('idle', 'before_backup'):
            with self.subTest(event=event):
                durable_json(self.directory / 'job.json', self.job)
                self.assertEqual(self.engine(event).resume()['phase'], 'FAILED')
                self.assertEqual((self.directory / 'application/VERSION').read_text(), 'old')
                self.assertFalse(self.fence.active(self.job['id']))

    def test_process_death_in_every_installation_mutation_stage(self):
        for event in ('before_install','after_migration','after_install','new_health'):
            with self.subTest(event=event):
                durable_json(self.directory / 'job.json', self.job)
                shutil.rmtree(self.directory / 'snapshot', ignore_errors=True)
                self.crash(event)
                self.assertEqual(self.engine().resume()['phase'], 'ROLLED_BACK')
                self.restored()

    def test_interrupted_preparation_aborts_without_installing(self):
        self.crash('after_backup')
        self.assertEqual(self.engine().resume()['phase'], 'FAILED')
        self.assertEqual((self.directory / 'application/VERSION').read_text(), 'old')

    def test_interrupted_restore_resumes_same_snapshot(self):
        self.crash('after_migration')
        self.crash('after_database_restore')
        self.assertEqual(self.engine().resume()['phase'], 'ROLLED_BACK')
        self.restored()
        self.assertEqual(self.engine().load()['rollback_resumes'], 2)

    def test_failed_rollback_stays_fenced_without_automatic_retry(self):
        self.crash('after_migration')
        self.assertEqual(self.engine('before_restore').resume()['phase'], 'RECOVERY_REQUIRED')
        self.assertTrue(self.fence.active(self.job['id']))
        self.assertEqual(self.engine().resume()['phase'], 'RECOVERY_REQUIRED')

    def test_rollback_resume_budget_is_bounded(self):
        self.crash('after_migration')
        job=self.engine().load();job['rollback_resumes']=8;durable_json(self.directory / 'job.json',job)
        self.assertEqual(self.engine().resume()['phase'], 'RECOVERY_REQUIRED')
        self.assertTrue(self.fence.active(self.job['id']))

    def test_commit_release_crash_never_rewinds_new_mutations(self):
        self.assertEqual(self.engine().resume()['phase'], 'SUCCEEDED')
        job=self.engine().load();job['phase']='COMMITTED';durable_json(self.directory / 'job.json',job)
        with contextlib.closing(sqlite3.connect(self.directory / 'application/database')) as database, database:
            database.execute("INSERT INTO data VALUES('post-release mutation')")
        self.assertEqual(self.engine().resume()['phase'], 'SUCCEEDED')
        with contextlib.closing(sqlite3.connect(self.directory / 'application/database')) as database, database:
            self.assertEqual(database.execute('SELECT count(*) FROM data').fetchone()[0],3)

    def test_os_lock_serializes_admission_and_persistent_marker_survives_exit(self):
        with (self.admission / 'admission.lock').open('rb') as incoming:
            fcntl.flock(incoming,fcntl.LOCK_SH|fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                with self.fence.hold(self.job['id']): pass
        with self.fence.hold(self.job['id']):
            with (self.admission / 'admission.lock').open('rb') as incoming:
                with self.assertRaises(BlockingIOError): fcntl.flock(incoming,fcntl.LOCK_SH|fcntl.LOCK_NB)
        self.assertTrue(self.fence.active(self.job['id']))


if __name__ == '__main__':
    if len(sys.argv)>1 and sys.argv[1]=='--crash':
        directory,admission,event=sys.argv[2:]
        Coordinator(directory,Fence(Path(admission)),FileHost(directory,crash=event)).resume()
    else:
        unittest.main()
