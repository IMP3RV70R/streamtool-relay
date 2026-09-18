"""Exercise the bundled SSH command locally with fixture paths/UID only.

This does not accept real SSH, Android TLS or installed-service readiness.
"""
import contextlib
import base64
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SOURCE = (ROOT / 'apps/android/app/src/main/java/dev/streamtool/app/SshConnection.kt').read_text()
SCRIPT = SOURCE.split("python3 - <<'STREAMTOOL_HANDOFF'", 1)[1].split('STREAMTOOL_HANDOFF', 1)[0]
SCRIPT = '\n'.join(line[12:] if line.startswith(' ' * 12) else line for line in SCRIPT.splitlines())


class HandoffTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'installation'; self.root.mkdir(mode=0o700)
        (self.root / 'state').mkdir(); (self.root / 'secrets').mkdir()
        (self.root / '.env').write_text('DOMAIN=8.8.8.8\n'); (self.root / '.env').chmod(0o600)
        self.token = base64.b64encode(os.urandom(32)).decode()
        self.token_file = self.root / 'secrets/setup_token'
        self.token_file.write_text(self.token + '\n'); self.token_file.chmod(0o600)
        self.db = self.root / 'state/control.sqlite'
        with contextlib.closing(sqlite3.connect(self.db)) as db: db.execute('CREATE TABLE installation(owner_id TEXT)')

    def run_command(self):
        script = SCRIPT.replace("Path('/opt/streamtool')", 'Path(' + repr(str(self.root)) + ')')
        script = script.replace("Path('/var/lib/streamtool-installer/application/state.json')", 'Path(' + repr(str(self.root/'job.json')) + ')')
        script = script.replace('os.geteuid()!=0', 'False').replace('info.st_uid not in (0,65532)', 'info.st_uid!=' + str(os.geteuid()))
        return subprocess.run([sys.executable, '-c', script], capture_output=True)

    def test_private_initial_token_and_origin(self):
        result = self.run_command(); self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout), {'protocol': 1, 'origin': 'https://8.8.8.8', 'setup_token': self.token})

    def test_existing_owner_never_reads_or_returns_token(self):
        with contextlib.closing(sqlite3.connect(self.db)) as db:
            db.execute("INSERT INTO installation VALUES('owner')"); db.commit()
        self.token_file.unlink()
        result = self.run_command(); self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)['setup_token'], '')

    def test_missing_database_does_not_create_a_new_one(self):
        self.db.unlink(); self.assertNotEqual(self.run_command().returncode, 0)
        self.assertFalse(self.db.exists())

    def test_nonprivate_or_symlink_token_is_refused(self):
        self.token_file.chmod(0o644); self.assertNotEqual(self.run_command().returncode, 0)
        external = Path(self.tmp.name) / 'external'; external.write_text(self.token); external.chmod(0o600)
        self.token_file.unlink(); self.token_file.symlink_to(external)
        self.assertNotEqual(self.run_command().returncode, 0)

    def test_initial_job_must_pass_readiness_before_token_transfer(self):
        job=self.root/'job.json';job.write_text('{"phase":"READY"}');job.chmod(0o600)
        result=self.run_command();self.assertNotEqual(result.returncode,0)
        self.assertNotIn(self.token.encode(),result.stdout)
        job.write_text('{"phase":"SUCCEEDED"}')
        self.assertEqual(self.run_command().returncode,0)

    def test_invalid_token_is_refused_without_returning_it(self):
        self.token_file.write_text('PRIVATE-INVALID-TOKEN')
        result = self.run_command(); self.assertNotEqual(result.returncode, 0)
        self.assertNotIn(b'PRIVATE-INVALID-TOKEN', result.stdout)


if __name__ == '__main__': unittest.main()
