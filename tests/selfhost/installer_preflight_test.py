"""Run the installer's actual subnet preflight against Docker-shaped responses."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

REPOSITORY=Path(__file__).resolve().parents[2]
SCRIPT=(REPOSITORY/'infra/selfhost/install.sh').read_text().split("python3 - <<'PY'\n",1)[1].split('\nPY',1)[0]


class InstallerPreflightTest(unittest.TestCase):
    def check(self,networks):
        with tempfile.TemporaryDirectory() as temporary:
            docker=Path(temporary)/'docker'
            docker.write_text('#!'+sys.executable+'\nimport os,sys\nprint("fixture" if sys.argv[2]=="ls" else os.environ["NETWORK_FIXTURE"])\n')
            docker.chmod(0o755)
            return subprocess.run([sys.executable,'-c',SCRIPT],env={**os.environ,'PATH':temporary+os.pathsep+os.environ['PATH'],'NETWORK_FIXTURE':json.dumps(networks)},capture_output=True)

    def test_default_null_ipam_and_host_none_networks(self):
        result=self.check([{'IPAM':None},{'IPAM':{'Config':None}},{'IPAM':{'Config':[]}},{'IPAM':{'Config':[{'Subnet':'172.17.0.0/16'}]}}])
        self.assertEqual(result.returncode,0)

    def test_any_enclosing_subnet_conflict_is_refused(self):
        result=self.check([{'IPAM':{'Config':[{'Subnet':'172.30.0.0/16'}]}}])
        self.assertNotEqual(result.returncode,0)
        self.assertIn(b'Docker subnet conflict',result.stderr)


if __name__=='__main__':unittest.main()
