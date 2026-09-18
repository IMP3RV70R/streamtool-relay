import hashlib
import importlib.util
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('release_archive', Path(__file__).resolve().parents[2] / 'infra/selfhost/archive.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PortableReleaseTests(unittest.TestCase):
    def test_exact_archive_contents_and_checksums(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'release'
            (root / 'web').mkdir(parents=True)
            (root / 'VERSION').write_text('0.1.0-test\n')
            (root / 'web/app.js').write_text('fixture')
            destination = Path(temporary) / 'release.tar.gz'
            module.package(root, destination)
            with tarfile.open(destination) as archive:
                self.assertEqual(set(archive.getnames()), {'VERSION', 'web/app.js', 'SHA256SUMS'})
                self.assertTrue(all(member.isfile() for member in archive.getmembers()))
                sums = archive.extractfile('SHA256SUMS').read().decode().splitlines()
                for line in sums:
                    self.assertEqual(hashlib.sha256(archive.extractfile(line[66:]).read()).hexdigest(), line[:64])
            before = destination.read_bytes()
            with self.assertRaises(FileExistsError):
                module.package(root, destination)
            self.assertEqual(destination.read_bytes(), before)

    def test_links_and_platform_metadata_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / 'release'
            root.mkdir()
            (root / 'bad').symlink_to('/etc/passwd')
            destination = Path(temporary) / 'release.tar.gz'
            with self.assertRaises(ValueError):
                module.package(root, destination)
            self.assertFalse(destination.exists())
            (root / 'bad').unlink()
            (root / '._VERSION').write_bytes(b'host metadata')
            with self.assertRaises(ValueError):
                module.package(root, destination)
            self.assertFalse(destination.exists())


if __name__ == '__main__':
    unittest.main()
