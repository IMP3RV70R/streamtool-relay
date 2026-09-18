"""Allocated-reserve isolation, interruption cleanup and bounded resume checks."""
import errno
import contextlib
import io
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
import uuid
from unittest.mock import Mock,patch

sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'infra/selfhost'))
import updater
from updater_host import DiskReserve


class ResumeTest(unittest.TestCase):
    def test_early_release_has_no_docker_or_application_dependency(self):
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);job=str(uuid.uuid4())
            (root/'current.json').write_text(json.dumps({'job':job}))
            with patch.object(updater,'STATE',root),patch.object(updater.os,'geteuid',return_value=0),patch.object(sys,'platform','linux'),patch.object(sys,'argv',['updater.py','release-space']),patch.object(DiskReserve,'release',return_value=False) as release,patch.object(updater,'coordinator',side_effect=AssertionError('application dependency')),patch.object(updater,'command',side_effect=AssertionError('Docker dependency')),contextlib.redirect_stdout(io.StringIO()):
                updater.main()
            release.assert_called_once_with()

    def test_enospc_retry_is_bounded_and_uses_durable_resume(self):
        with tempfile.TemporaryDirectory() as temporary:
            job=str(uuid.uuid4())
            engine=Mock();engine.resume.side_effect=[OSError(errno.ENOSPC,'full'),OSError(errno.ENOSPC,'still full')]
            with patch.object(updater,'STATE',Path(temporary)),patch.object(updater,'coordinator',return_value=engine),patch.object(DiskReserve,'release',side_effect=[False,True]) as release:
                with self.assertRaises(OSError):updater.resume_job(job)
            self.assertEqual(engine.resume.call_count,2)
            self.assertEqual(release.call_args_list[-1].kwargs,{'force':True})

    def test_no_reserve_does_not_repeat_failed_transaction(self):
        engine=Mock();engine.resume.side_effect=OSError(errno.ENOSPC,'full')
        with patch.object(updater,'coordinator',return_value=engine),patch.object(DiskReserve,'release',return_value=False):
            with self.assertRaises(OSError):updater.resume_job(str(uuid.uuid4()))
        self.assertEqual(engine.resume.call_count,1)

    def test_other_errors_do_not_release_reserve_or_retry(self):
        engine=Mock();engine.resume.side_effect=OSError(errno.EIO,'device error')
        with patch.object(updater,'coordinator',return_value=engine),patch.object(DiskReserve,'release',return_value=False) as release:
            with self.assertRaises(OSError):updater.resume_job(str(uuid.uuid4()))
        self.assertEqual(engine.resume.call_count,1);self.assertEqual(release.call_count,1)


@unittest.skipUnless(sys.platform=='linux','real Linux posix_fallocate required')
class ReserveTest(unittest.TestCase):
    def setUp(self):
        self.temporary=tempfile.TemporaryDirectory();self.addCleanup(self.temporary.cleanup)
        self.root=Path(self.temporary.name);self.job=self.root/str(uuid.uuid4());self.job.mkdir()
        self.anchor=self.root/'volume';self.anchor.mkdir()
        self.reserve=DiskReserve(self.job)

    def test_real_allocation_deduplicates_and_preserves_unrelated_data(self):
        self.reserve.allocate((self.anchor,self.root),8<<20)
        inventory=json.loads(self.reserve.inventory.read_text())
        self.assertEqual(len(inventory['entries']),1)
        path=Path(inventory['entries'][0]['path'])
        self.assertGreaterEqual(path.stat().st_blocks*512,8<<20)
        unrelated=path.parent/'unrelated';unrelated.write_bytes(b'keep')
        self.assertTrue(self.reserve.release(force=True))
        self.assertFalse(self.reserve.release(force=True))
        self.assertEqual(unrelated.read_bytes(),b'keep')

    def test_interrupted_allocation_is_tracked_and_cleanable(self):
        with patch('updater_host.os.posix_fallocate',side_effect=OSError(errno.ENOSPC,'full')):
            with self.assertRaises(OSError):self.reserve.allocate((self.anchor,),8<<20)
        self.assertTrue(self.reserve.inventory.exists())
        self.assertTrue(self.reserve.release(force=True))

    def test_separate_filesystems_have_independent_allocated_reserves(self):
        if not Path('/dev/shm').is_dir():self.skipTest('second Linux filesystem unavailable')
        with tempfile.TemporaryDirectory(dir='/dev/shm') as second:
            if os.stat(second).st_dev==self.anchor.stat().st_dev:self.skipTest('same filesystem')
            self.reserve.allocate((self.anchor,Path(second)),8<<20)
            entries=json.loads(self.reserve.inventory.read_text())['entries']
            self.assertEqual(len(entries),2)
            for entry in entries:self.assertGreaterEqual(Path(entry['path']).stat().st_blocks*512,8<<20)
            self.assertTrue(self.reserve.release(force=True))
            self.assertTrue(all(not Path(entry['path']).exists() for entry in entries))

    def test_directory_substitution_cannot_delete_external_file(self):
        self.reserve.allocate((self.anchor,),8<<20)
        path=Path(json.loads(self.reserve.inventory.read_text())['entries'][0]['path'])
        outside=self.root/'outside';outside.mkdir(mode=0o700)
        victim=outside/path.name;victim.write_bytes(b'preserve external data')
        original=DiskReserve.open_parent
        old=self.anchor/'old-reserve-directory'
        def swapped(candidate,device):
            descriptor=original(candidate,device)
            candidate.parent.rename(old)
            candidate.parent.symlink_to(outside)
            return descriptor
        with patch.object(DiskReserve,'open_parent',side_effect=swapped):
            self.assertTrue(self.reserve.release(force=True))
        self.assertEqual(victim.read_bytes(),b'preserve external data')
        self.assertFalse((old/path.name).exists())

    def test_unsafe_directory_and_linked_file_are_refused(self):
        outside=self.root/'outside';outside.mkdir()
        (self.anchor/'.streamtool-recovery-space').symlink_to(outside)
        with self.assertRaises(ValueError):self.reserve.allocate((self.anchor,),8<<20)
        (self.anchor/'.streamtool-recovery-space').unlink()
        self.reserve.allocate((self.anchor,),8<<20)
        path=Path(json.loads(self.reserve.inventory.read_text())['entries'][0]['path'])
        shared=self.root/'shared';os.link(path,shared)
        with self.assertRaises(ValueError):self.reserve.release(force=True)
        self.assertTrue(shared.exists());shared.unlink()
        self.assertTrue(self.reserve.release(force=True))

if __name__=='__main__':unittest.main()
