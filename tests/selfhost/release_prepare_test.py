"""Actual Ed25519/Go staging integration; fixture OCI is not executable acceptance."""
import argparse
import base64
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'infra/selfhost'))
from archive import package
spec=importlib.util.spec_from_file_location('release_prepare',ROOT/'infra/release/prepare.py')
prepare=importlib.util.module_from_spec(spec);spec.loader.exec_module(prepare)


class ReleaseTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build=tempfile.TemporaryDirectory();cls.tool=Path(cls.build.name)/'release-tool'
        subprocess.run(['go','build','-trimpath','-o',str(cls.tool),'./cmd/release-tool'],cwd=ROOT/'backend',check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

    @classmethod
    def tearDownClass(cls):cls.build.cleanup()

    def fixture(self,directory):
        seed=os.urandom(32);key=directory/'seed';key.write_bytes(base64.b64encode(seed));key.chmod(0o600)
        public=subprocess.run(['openssl','pkey','-inform','DER','-pubout','-outform','DER'],input=bytes.fromhex('302e020100300506032b657004220420')+seed,check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL).stdout[-32:]
        trust=directory/'trust.json';trust.write_text(json.dumps({'public_key':base64.b64encode(public).decode(),'channel':'stable','artifact_origin':'https://release.example.invalid'}));trust.chmod(0o600)
        for arch in ('amd64','arm64'):
            tree=directory/arch;tree.mkdir()
            for source in (ROOT/'infra/selfhost').iterdir():
                if source.is_file():(tree/source.name).write_bytes(source.read_bytes())
            for name in ('streamtool-agent.service','streamtool-agent.sudoers','streamtool-worker-policy'):(tree/name).write_bytes((ROOT/'infra/vm'/name).read_bytes())
            (tree/'web').mkdir()
            for name in ('index.html','app.js','style.css'):(tree/'web'/name).write_bytes(b'fixture')
            for name in ('node-agent','release-tool','images.tar'):(tree/name).write_bytes(b'fixture only; never execute')
            for name in ('api-image','worker-image','proxy-image','edge-image'):(tree/name).write_text('sha256:'+'a'*64+'\n')
            (tree/'VERSION').write_text('test-release\n');(tree/'ARCHITECTURE').write_text(arch+'\n')
            package(tree,directory/('streamtool-relay-test-release-'+arch+'.tar.gz'))
        return argparse.Namespace(tool=self.tool,key=key,trust=trust,output=directory/'signed',version='test-release',sequence=2,
            minimum_sequence=0,minimum_schema=0,maximum_schema=7,target_schema=7,valid_days=7,notes=None,
            base_url='https://release.example.invalid/download/test-release',
            amd64=directory/'streamtool-relay-test-release-amd64.tar.gz',arm64=directory/'streamtool-relay-test-release-arm64.tar.gz')

    def test_actual_sign_verify_both_architectures_and_no_secret_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            args=self.fixture(Path(temporary));original=args.key.read_bytes()
            prepare.prepare(args)
            manifest=json.loads((args.output/'manifest.json').read_text())
            self.assertEqual([a['architecture'] for a in manifest['artifacts']],['amd64','arm64'])
            self.assertEqual(set(p.name for p in args.output.iterdir()),{'manifest.json','manifest.sig'})
            self.assertNotIn(original,(args.output/'manifest.json').read_bytes())
            self.assertEqual(args.key.read_bytes(),original)
            with self.assertRaises(FileExistsError):prepare.prepare(args)

    def test_mismatched_bundle_architecture_removes_incomplete_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory=Path(temporary);args=self.fixture(directory)
            (directory/'arm64/ARCHITECTURE').write_text('amd64\n');args.arm64.unlink();package(directory/'arm64',args.arm64)
            with self.assertRaises(subprocess.CalledProcessError):prepare.prepare(args)
            self.assertFalse(args.output.exists());self.assertTrue(args.key.exists())

    def test_wrong_public_key_refuses_signed_output(self):
        with tempfile.TemporaryDirectory() as temporary:
            args=self.fixture(Path(temporary));trust=json.loads(args.trust.read_text());trust['public_key']=base64.b64encode(bytes(32)).decode();args.trust.write_text(json.dumps(trust))
            with self.assertRaises(subprocess.CalledProcessError):prepare.prepare(args)
            self.assertFalse(args.output.exists());self.assertTrue(args.key.exists())

if __name__=='__main__':unittest.main()
