"""Durable host-job tests; fake operations do not accept actual apt/systemd installation."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('installer',ROOT/'infra/selfhost/installer.py')
installer = importlib.util.module_from_spec(spec);spec.loader.exec_module(installer)


class InstallerJobTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.journal = installer.Journal(Path(self.tmp.name)/'journal')
        self.preparation = installer.Preparation(self.journal,check=lambda: None,execute=lambda *a,**k:None)
        self.docker = patch.object(installer.shutil,'which',return_value='/usr/bin/docker');self.docker.start();self.addCleanup(self.docker.stop)
    def test_idempotent_start_and_complete(self):
        first = self.preparation.begin();self.assertEqual(first,self.preparation.begin())
        calls=[]
        steps=[(p,lambda p=p:calls.append(p)) for p in ['PREREQUISITES','DOCKER','VERIFY']]
        self.assertEqual(self.preparation.run(steps)['phase'],'PREPARED')
        self.preparation.run(steps);self.assertEqual(calls,['PREREQUISITES','DOCKER','VERIFY'])
        self.assertEqual(self.journal.read()['job_id'],first['job_id'])
        self.assertEqual((self.journal.root/'state.json').stat().st_mode&0o777,0o600)
    def test_interruption_resumes_durable_phase(self):
        self.preparation.begin();calls=[]
        def killed():raise SystemExit(9)
        with self.assertRaises(SystemExit):self.preparation.run([('PREREQUISITES',lambda:calls.append('first')),('DOCKER',killed),('VERIFY',lambda:None)])
        self.assertEqual(self.journal.read()['phase'],'DOCKER')
        new=installer.Preparation(installer.Journal(self.journal.root),check=lambda:None)
        new.run([('PREREQUISITES',lambda:calls.append('repeated')),('DOCKER',lambda:calls.append('resumed')),('VERIFY',lambda:None)])
        self.assertEqual(calls,['first','resumed'])
    def test_bounded_failures_and_explicit_retry(self):
        first=self.preparation.begin()
        def fail():raise RuntimeError('SECRET must not enter status')
        for _ in range(3):
            with self.assertRaises(RuntimeError):self.preparation.run([('PREREQUISITES',fail)])
        state=self.journal.read();self.assertEqual(state['phase'],'FAILED')
        self.assertNotIn('SECRET',str(state));self.assertEqual(state['attempts'],3)
        self.preparation.run([('PREREQUISITES',fail)])
        retry=self.preparation.begin();self.assertEqual(first['job_id'],retry['job_id']);self.assertEqual(retry['attempts'],0)
    def test_expired_job_never_mutates(self):
        state=self.preparation.begin();state['deadline']=1;self.journal.write(state)
        self.assertEqual(self.preparation.run([('PREREQUISITES',lambda:self.fail('expired mutation'))])['phase'],'FAILED')
    def test_refused_host_does_not_create_job(self):
        self.preparation.check=lambda:(_ for _ in ()).throw(ValueError('unsupported'))
        with self.assertRaises(ValueError):self.preparation.begin()
        self.assertIsNone(self.journal.read())
    def test_exclusive_job_lock(self):
        with self.journal.lock():
            with self.assertRaises(BlockingIOError):self.preparation.begin()
    def test_corruption_and_symlink_fail_closed(self):
        self.preparation.begin();path=self.journal.root/'state.json';path.write_text('{}')
        with self.assertRaises(ValueError):self.preparation.begin()
        path.unlink();path.symlink_to(Path(self.tmp.name)/'external')
        with self.assertRaises(OSError):self.journal.read()
    def test_foreign_service_is_not_overwritten(self):
        service=Path(self.tmp.name)/'service';service.write_text('user-owned custom service')
        with patch.object(installer.os,'geteuid',return_value=0), patch.object(installer.sys,'argv',['installer','prepare']), patch.object(installer,'host_checks',return_value=('debian','trixie')), patch.object(installer,'SERVICE',service), patch.object(installer,'TIMER',Path(self.tmp.name)/'timer'), patch.object(installer,'atomic') as write:
            with self.assertRaises(ValueError):installer.main()
            write.assert_not_called()
        self.assertEqual(service.read_text(),'user-owned custom service')


if __name__=='__main__':unittest.main()
