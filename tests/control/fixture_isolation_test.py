"""Local fixture cleanup must not target the installed self-hosted node."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
PRODUCTION_NODE = '00000000-0000-4000-8000-000000000001'


class FixtureIsolationTest(unittest.TestCase):
    def execute(self, script, node=None):
        with tempfile.TemporaryDirectory(prefix='streamtool-fixture-node-') as tmp:
            tmp = Path(tmp)
            (tmp/'infra/local/secrets').mkdir(parents=True)
            certs = tmp/'.artifacts/control-certs'
            certs.mkdir(parents=True)
            for name in ['controller.key', 'agent.key']:
                (certs/name).touch()
            # TLS generation is covered elsewhere; this checks deployment routing.
            openssl = tmp/'openssl'
            openssl.write_text('#!/bin/sh\nexit 0\n')
            openssl.chmod(0o755)
            docker = tmp/'docker'
            docker.write_text('''#!/usr/bin/env python3
import json, os, sys
with open(os.environ['DOCKER_CALLS'], 'a') as output:
    output.write(json.dumps(sys.argv[1:])+'\\n')
''')
            docker.chmod(0o755)
            env = dict(os.environ, PATH=str(tmp)+os.pathsep+os.environ['PATH'],
                       DOCKER_CALLS=str(tmp/'calls.jsonl'))
            env.pop('CONTROL_TEST_NODE_ID', None)
            if node is not None:
                env['CONTROL_TEST_NODE_ID'] = node
            result = subprocess.run(['sh', str(ROOT/'tests/control'/script)],
                                    cwd=tmp, env=env, capture_output=True, timeout=15)
            config = tmp/'.artifacts/control-media-nodes.json'
            calls = tmp/'calls.jsonl'
            return (result.returncode, json.loads(config.read_text()) if config.exists() else None,
                    [json.loads(line) for line in calls.read_text().splitlines()] if calls.exists() else [])

    def test_default_deployment_separate_from_installed_node(self):
        code, nodes, _ = self.execute('certificates.sh')
        self.assertEqual(code, 0)
        self.assertNotEqual(nodes[0]['id'], PRODUCTION_NODE)

    def test_explicit_test_node_propagated(self):
        node = '10306099-9d97-438b-b9b6-9413f32872b4'
        code, nodes, _ = self.execute('certificates.sh', node)
        self.assertEqual(code, 0)
        self.assertEqual(nodes[0]['id'], node)

    def test_installed_node_refused_before_deployment(self):
        code, nodes, _ = self.execute('certificates.sh', PRODUCTION_NODE)
        self.assertNotEqual(code, 0)
        self.assertIsNone(nodes)

    def test_default_cleanup_separate_from_installed_node(self):
        code, _, calls = self.execute('down.sh')
        self.assertEqual(code, 0)
        filters = [arg for call in calls for arg in call if arg.startswith('label=streamtool.node=')]
        self.assertEqual(len(filters), 1)
        self.assertNotEqual(filters[0], 'label=streamtool.node='+PRODUCTION_NODE)

    def test_cleanup_refuses_installed_node_before_docker(self):
        code, _, calls = self.execute('down.sh', PRODUCTION_NODE)
        self.assertNotEqual(code, 0)
        self.assertEqual(calls, [])


if __name__ == '__main__':
    unittest.main()
