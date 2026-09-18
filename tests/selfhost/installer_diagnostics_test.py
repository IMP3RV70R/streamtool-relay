"""Diagnostics never disclose provider output, paths, command lines or secrets."""
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import patch
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'infra/selfhost'))
import installer_diagnostics as diagnostics

class DiagnosticsTest(unittest.TestCase):
    def test_service_is_bounded_and_allowlisted(self):
        with patch.object(subprocess, 'check_output', return_value=b'ActiveState=failed\nResult=exit-code\nExecMainStatus=1\nEnvironment=PASSWORD\nSubState=secret/path\n') as probe:
            result = diagnostics.service('docker.service')
        self.assertEqual(result, {'ActiveState': 'failed', 'Result': 'exit-code', 'ExecMainStatus': '1'})
        self.assertEqual(probe.call_args.kwargs['timeout'], 3)
        self.assertNotIn('PASSWORD', json.dumps(result))
    def test_unavailable_docker_does_not_disclose_exception(self):
        with patch.object(diagnostics, 'service', return_value={}), patch.object(subprocess, 'run', side_effect=subprocess.CalledProcessError(1, ['PASSWORD'], stderr=b'SECRET')):
            result = diagnostics.collect()
        self.assertFalse(result['docker_available'])
        self.assertNotIn('PASSWORD', json.dumps(result)); self.assertNotIn('SECRET', json.dumps(result))

if __name__ == '__main__': unittest.main()
