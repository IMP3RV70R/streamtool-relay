"""Website replacement must survive independent root/journal filesystems."""
import errno
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'infra/selfhost'))
from updater_host import LinuxHost


class WebReplacementTests(unittest.TestCase):
    def fixture(self,path):
        root=path/'root';(root/'web').mkdir(parents=True);(root/'web/index.html').write_text('old')
        journal=path/'journal';journal.mkdir()
        source=path/'source';source.mkdir();(source/'index.html').write_text('new')
        return LinuxHost(root,journal),source

    def test_different_journal_volume_does_not_require_cross_device_rename(self):
        with tempfile.TemporaryDirectory() as temporary:
            host,source=self.fixture(Path(temporary));original=Path.rename
            def rename(path,destination):
                if Path(destination).is_relative_to(host.directory):raise OSError(errno.EXDEV,'separate journal device')
                return original(path,destination)
            with patch.object(Path,'rename',rename):host.replace_web(source,'previous-web')
            self.assertEqual((host.root/'web/index.html').read_text(),'new')
            previous=list(host.root.glob('web.updater-previous-web-*'))
            self.assertEqual(len(previous),1)
            self.assertEqual((previous[0]/'index.html').read_text(),'old')

    def test_failed_final_rename_can_be_restored_from_matching_recovery_website(self):
        with tempfile.TemporaryDirectory() as temporary:
            host,source=self.fixture(Path(temporary));original=Path.rename
            def rename(path,destination):
                if path.name=='web.updater-new':raise OSError(errno.EIO,'interrupted final rename')
                return original(path,destination)
            with patch.object(Path,'rename',rename):
                with self.assertRaises(OSError):host.replace_web(source,'previous-web')
            recovery=Path(temporary)/'recovery-web';recovery.mkdir();(recovery/'index.html').write_text('old')
            host.replace_web(recovery,'failed-web')
            self.assertEqual((host.root/'web/index.html').read_text(),'old')

if __name__=='__main__':unittest.main()
