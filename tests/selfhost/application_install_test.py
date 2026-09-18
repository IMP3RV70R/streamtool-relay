"""Durable initial-install faults with real bootstrap files, fake host operations."""
import base64
import hashlib
import tarfile
import json
import os
from pathlib import Path
import shutil
import subprocess
import io
import urllib.request
import sys
import tempfile
import unittest
from unittest.mock import patch
import uuid
import textwrap
import zipfile

REPO = Path(__file__).resolve().parents[2]
sys.path.insert(0,str(REPO/'infra/selfhost'))
import deploy, installer_application as app


class InstallationTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.directory = Path(self.tmp.name)
        self.assets = self.directory/'assets'; self.assets.mkdir()
        self.config = {'public_key':base64.b64encode(os.urandom(32)).decode(),'channel':'preview',
                       'artifact_origin':'https://release.example.invalid','manifest_url':'https://release.example.invalid/manifest.json',
                       'signature_url':'https://release.example.invalid/manifest.sig'}
        (self.assets/'distribution.json').write_text(json.dumps(self.config))
        self.journal = app.Journal(self.directory/'job')
        self.installation = app.Installation(self.journal,self.assets,check=lambda:None)
    def operations(self, calls, killed=None):
        def step(phase):
            def action(state):
                calls.append(phase)
                if phase == killed: raise SystemExit(9)
                if phase == 'METADATA': state.update(version='test-release',sequence=1,schema=7)
            return action
        return {p:step(p) for p in app.PHASES}
    def test_every_phase_resumes_without_replaying_earlier_operations(self):
        for phase in app.PHASES:
            with self.subTest(phase=phase):
                journal=app.Journal(self.directory/phase)
                installation=app.Installation(journal,self.assets,check=lambda:None)
                first=installation.begin('8.8.8.8'); calls=[]
                with self.assertRaises(SystemExit): installation.run(self.operations(calls,killed=phase))
                self.assertEqual(journal.read()['phase'],phase)
                new=app.Installation(app.Journal(journal.root),self.assets,check=lambda:self.fail('repeated preflight'))
                new.run(self.operations(calls))
                self.assertEqual(journal.read()['phase'],'SUCCEEDED')
                self.assertEqual(journal.read()['job_id'],first['job_id'])
                self.assertEqual(calls,app.PHASES[:app.PHASES.index(phase)]+[phase]+app.PHASES[app.PHASES.index(phase):])
                new.run(self.operations(calls)); self.assertEqual(journal.read()['attempts'],2)
    def test_budget_and_retry_preserve_failed_phase_and_identity(self):
        first=self.installation.begin('8.8.8.8')
        def fail(state): raise ValueError('PASSWORD MUST NOT ENTER STATUS')
        operations=self.operations([]);operations['CONFIGURE']=fail
        for _ in range(3):
            with self.assertRaises(ValueError): self.installation.run(operations)
        state=self.journal.read();self.assertEqual(state['phase'],'FAILED');self.assertEqual(state['resume_phase'],'CONFIGURE')
        self.assertNotIn('PASSWORD',json.dumps(state))
        retry=self.installation.begin('8.8.8.8');self.assertEqual(retry['job_id'],first['job_id']);self.assertEqual(retry['phase'],'CONFIGURE')
        calls=[];self.installation.run(self.operations(calls));self.assertEqual(calls,app.PHASES[app.PHASES.index('CONFIGURE'):])
    def test_command_failures_expose_only_safe_code_not_argv_or_provider_output(self):
        operations = self.operations([])
        error = subprocess.CalledProcessError(17, ['docker', 'load', 'PASSWORD'], stderr=b'SECRET')
        def fail(state): raise error
        operations['IMAGES'] = fail
        with self.assertRaises(subprocess.CalledProcessError): self.installation.begin('8.8.8.8'); self.installation.run(operations)
        state = self.journal.read()
        self.assertEqual(state['error'], 'command_failed')
        self.assertEqual(state['failure'], {'command': 'docker', 'exit_code': 17})
        self.assertNotIn('PASSWORD', json.dumps(state)); self.assertNotIn('SECRET', json.dumps(state))
        code, details = app.failure_details(subprocess.TimeoutExpired(['docker', 'SECRET'], 900))
        self.assertEqual(code, 'command_timeout'); self.assertEqual(details, {'command': 'docker'})

    def test_expiry_and_changed_distribution_never_mutate(self):
        state=self.installation.begin('8.8.8.8');state['deadline']=1;self.journal.write(state)
        self.installation.run(self.operations([]));self.assertEqual(self.journal.read()['phase'],'FAILED')
        self.installation.begin('8.8.8.8');(self.assets/'distribution.json').write_text('{}')
        with self.assertRaises(ValueError):self.installation.run(lambda:self.fail('changed trust executed'))
        self.assertEqual(self.journal.read()['attempts'],1)
    def test_other_ip_or_trust_and_foreign_host_are_refused(self):
        self.installation.begin('8.8.8.8')
        with self.assertRaises(ValueError):self.installation.begin('1.1.1.1')
        self.config['channel']='stable';(self.assets/'distribution.json').write_text(json.dumps(self.config))
        with self.assertRaises(ValueError):self.installation.begin('8.8.8.8')
        fresh=app.Installation(app.Journal(self.directory/'foreign'),self.assets,check=lambda:(_ for _ in ()).throw(ValueError()))
        with self.assertRaises(ValueError):fresh.begin('8.8.8.8')
        self.assertIsNone(fresh.journal.read())
    def test_unconfigured_distribution_and_nonpublic_ip_are_refused(self):
        for ip in ['127.0.0.1','192.0.2.1','ff02::1','::ffff:8.8.8.8']:
            with self.assertRaises(ValueError):self.installation.begin(ip)
        (self.assets/'distribution.json').write_text('{"enabled":false}')
        with self.assertRaises(ValueError):self.installation.begin('8.8.8.8')
        self.assertIsNone(self.journal.read())


class ConfigurationTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.directory=Path(self.tmp.name);self.stage=self.directory/'release';self.stage.mkdir()
        for name in ['bootstrap.py','compose.yml','Caddyfile','mediamtx.yml']:shutil.copyfile(REPO/'infra/selfhost'/name,self.stage/name)
        shutil.copytree(REPO/'apps/web',self.stage/'web')
        for name in ['api','worker','proxy','edge']:(self.stage/(name+'-image')).write_text('sha256:'+'a'*64)
        with tarfile.open(self.stage / 'images.tar', 'w'): pass
        self.root=self.directory/'streamtool'
        self.job={'job_id':str(uuid.uuid4()),'address':'8.8.8.8','version':'test-release','sequence':2,'schema':7}
        self.deployment=deploy.InitialDeployment(self.root,self.stage,self.job,execute=lambda *a,**k:None)
    def test_loading_bootstrap_does_not_mutate_verified_stage(self):
        before = {p.relative_to(self.stage).as_posix(): p.read_bytes() for p in self.stage.rglob('*') if p.is_file()}
        deploy.InitialDeployment(self.root, self.stage, self.job, execute=lambda *a, **k: None)
        after = {p.relative_to(self.stage).as_posix(): p.read_bytes() for p in self.stage.rglob('*') if p.is_file()}
        self.assertEqual(after, before)
        self.assertFalse((self.stage / '__pycache__').exists())

    def oci_archive(self):
        data = json.dumps({'schemaVersion': 2, 'config': {'digest': 'sha256:' + 'a' * 64}, 'layers': []}).encode()
        stamp = 'sha256:' + hashlib.sha256(data).hexdigest()
        index = json.dumps({'manifests': [{'digest': stamp}]}).encode()
        with tarfile.open(self.stage / 'images.tar', 'w') as archive:
            for name, content in [('index.json', index), ('blobs/sha256/' + stamp[7:], data)]:
                entry = tarfile.TarInfo(name); entry.size = len(content); archive.addfile(entry, io.BytesIO(content))
        return stamp
    def test_containerd_resolves_only_signed_manifest_and_persists_for_resume(self):
        stamp = self.oci_archive()
        def inspect(args, **kwargs):
            if args[-1] == 'sha256:' + 'a' * 64: raise subprocess.CalledProcessError(1, args)
            self.assertEqual(args[-1], stamp)
            return 'linux/amd64'
        with patch.object(deploy.platform, 'machine', return_value='x86_64'), patch.object(deploy.subprocess, 'check_output', side_effect=inspect):
            self.deployment.image_load()
        self.assertEqual(self.job['runtime_images'], {name: stamp for name in ('api','worker','proxy','edge')})
        resumed = deploy.InitialDeployment(self.root, self.stage, self.job, execute=lambda *a, **k: None)
        self.assertEqual(resumed.images, self.job['runtime_images'])
        self.job['runtime_images']['worker'] = 'sha256:' + 'b' * 64
        with self.assertRaises(ValueError): deploy.InitialDeployment(self.root, self.stage, self.job)
    def test_bad_oci_digest_is_never_used(self):
        self.oci_archive()
        archive = self.stage / 'images.tar'
        data = archive.read_bytes().replace(b'"schemaVersion": 2', b'"schemaVersion": 3')
        archive.write_bytes(data)
        with self.assertRaises(ValueError): deploy.manifest_ids(archive, self.deployment.declared_images)

    def secrets(self,root):
        return {p.relative_to(root).as_posix():p.read_bytes() for p in root.rglob('*') if p.is_file() and (p.suffix=='.key' or p.parent.name=='secrets')}
    def test_partial_bootstrap_resumes_same_keys_and_publishes_correct_paths(self):
        def killed(*args,**kwargs):raise SystemExit(9)
        with patch.object(self.deployment.bootstrap.shutil,'copyfile',side_effect=killed):
            with self.assertRaises(SystemExit):self.deployment.configure()
        staging=self.root.parent/('.streamtool-bootstrap-'+self.job['job_id']); before=self.secrets(staging)
        self.assertTrue((staging/'api.env').exists());self.assertFalse(self.root.exists())
        deploy.InitialDeployment(self.root,self.stage,self.job).configure()
        self.assertEqual(before,self.secrets(self.root))
        self.assertIn(str(self.root/'certs/agent.key'),(self.root/'node.env').read_text())
        self.assertNotIn(str(staging),(self.root/'node.env').read_text())
        (self.root/'state/control.sqlite').write_bytes(b'OWNER STATE')
        (self.root/'api.env').write_text('USER CONFIGURATION')
        self.deployment.configure()
        self.assertEqual((self.root/'api.env').read_text(),'USER CONFIGURATION')
        self.assertEqual((self.root/'state/control.sqlite').read_bytes(),b'OWNER STATE')
    def test_interrupt_after_atomic_publish_never_reinitializes(self):
        sync=deploy.sync
        def killed(path):
            if path==self.root.parent and self.root.exists():raise SystemExit(9)
            sync(path)
        with patch.object(deploy,'sync',side_effect=killed):
            with self.assertRaises(SystemExit):self.deployment.configure()
        before=self.secrets(self.root);self.deployment.configure();self.assertEqual(before,self.secrets(self.root))
    def test_interruption_before_stage_publication_does_not_strand_recovery(self):
        publish=deploy.publish
        def killed(source,target):
            if target.name.startswith('.streamtool-bootstrap-'):raise SystemExit(9)
            publish(source,target)
        with patch.object(deploy,'publish',side_effect=killed):
            with self.assertRaises(SystemExit):self.deployment.configure()
        self.assertFalse(self.root.exists())
        self.deployment.configure()
        self.assertTrue((self.root/'secrets/envelope_key').exists())
    def test_foreign_root_stage_and_destination_are_preserved(self):
        self.root.mkdir();(self.root/'INSTALLATION_JOB').write_text(str(uuid.uuid4()))
        with self.assertRaises(ValueError):self.deployment.configure()
        foreign=self.directory/'foreign';foreign.write_text('USER DATA')
        with self.assertRaises(ValueError):self.deployment.copy(self.stage/'bootstrap.py',foreign,0o700)
        self.assertEqual(foreign.read_text(),'USER DATA')
    def test_actual_image_platform_must_match_before_configuration(self):
        with patch.object(deploy.platform,'machine',return_value='x86_64'), patch.object(deploy.subprocess,'check_output',return_value='linux/arm64'):
            with self.assertRaises(ValueError):self.deployment.image_load()
            self.assertFalse(self.root.exists())
        with patch.object(deploy.platform,'machine',return_value='x86_64'), patch.object(deploy.subprocess,'check_output',return_value='linux/amd64') as inspect:
            self.deployment.image_load();self.assertEqual(inspect.call_count,4)
    def test_approved_copy_is_atomic_and_replayable(self):
        target=self.directory/'host/file';self.deployment.copy(self.stage/'bootstrap.py',target,0o700)
        self.deployment.copy(self.stage/'bootstrap.py',target,0o700)
        self.assertEqual(target.read_bytes(),(self.stage/'bootstrap.py').read_bytes())


class DownloadTest(unittest.TestCase):
    def test_https_origin_and_cdn_redirect_restrictions(self):
        redirects=app.Redirects('https://github.com')
        request=urllib.request.Request('https://github.com/owner/repo/releases/download/test/archive')
        allowed='https://release-assets.githubusercontent.com/path?temporary=credential'
        self.assertEqual(redirects.redirect_request(request,None,302,'',{},allowed).full_url,allowed)
        explicit=app.Redirects('https://github.com:443')
        self.assertEqual(explicit.redirect_request(request,None,302,'',{},allowed).full_url,allowed)
        for url in ['http://github.com/file','https://evil.example/file','https://release-assets.githubusercontent.com.evil.example/file',
                    'https://release-assets.githubusercontent.com:8443/file','https://user:secret@github.com/file']:
            with self.subTest(url=url),self.assertRaises(ValueError):redirects.redirect_request(request,None,302,'',{},url)
    def test_stream_limits_hash_and_atomic_download(self):
        class Response(io.BytesIO): status=200
        with tempfile.TemporaryDirectory() as temporary:
            destination=Path(temporary)/'bundle'
            opener=unittest.mock.Mock();opener.open.side_effect=lambda *a,**k:Response(b'payload')
            with patch.object(app.urllib.request,'build_opener',return_value=opener):
                app.download('https://release.example.invalid/file',destination,7,'https://release.example.invalid',7,app.hashlib.sha256(b'payload').hexdigest())
                self.assertEqual(destination.read_bytes(),b'payload')
                with self.assertRaises(ValueError):app.download('https://release.example.invalid/file',destination,6,'https://release.example.invalid')
                with self.assertRaises(ValueError):app.download('https://release.example.invalid/file',destination,7,'https://release.example.invalid',7,'a'*64)
                self.assertEqual(destination.read_bytes(),b'payload')
                self.assertFalse((destination.parent/'.download-bundle').exists())


class DeliveryTest(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.directory=Path(self.tmp.name)
        self.journal=self.directory/'journal/application';self.assets=self.directory/'libexec/installer'
        source=(REPO/'apps/android/app/src/main/java/dev/streamtool/app/SshConnection.kt').read_text()
        self.script=textwrap.dedent(source.split('fun install(payload:')[1].split('val bootstrap = """')[1].split('""".trimIndent()',1)[0])
        self.script=self.script.replace('/var/lib/streamtool-installer/application',str(self.journal)).replace('/usr/local/libexec/streamtool-installer',str(self.assets))
        self.script=self.script.replace('os.geteuid()!=0','False').replace('info.st_uid!=0','info.st_uid!=os.geteuid()').replace('/usr/bin/python3',sys.executable)
    def payload(self,extra=None,changed=False):
        data=io.BytesIO()
        with zipfile.ZipFile(data,'w') as archive:
            for name in ['installer.py','installer_application.py','deploy.py','distribution.json','release-tool-amd64','release-tool-arm64']:
                # Only the delivery layer is exercised; no root deployment here.
                content=b'import sys,json; print(json.dumps({"protocol":1,"phase":"PENDING","address":json.loads(sys.stdin.buffer.read())["address"]}))' if name=='installer_application.py' else b'changed' if changed else b'fixture'
                archive.writestr(name,content)
            if extra:archive.writestr(extra,b'UNTRUSTED')
        return data.getvalue()
    def deliver(self,payload,expected=None):
        request=json.dumps({'address':'8.8.8.8'}).encode()
        script=self.script.replace('$digest',app.hashlib.sha256(expected or payload).hexdigest())
        return subprocess.run([sys.executable,'-c',script],input=len(request).to_bytes(4,'big')+request+payload,capture_output=True)
    def test_valid_payload_replays_and_preparation_parent_remains_private(self):
        payload=self.payload()
        for _ in range(2):
            result=self.deliver(payload);self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(json.loads(result.stdout)['address'],'8.8.8.8')
        self.assertEqual(self.journal.parent.stat().st_mode & 0o077,0)
        import installer
        with installer.Journal(self.journal.parent).lock():pass
    def test_wrong_digest_makes_no_helper_or_journal(self):
        payload=self.payload();result=self.deliver(payload+b'changed',expected=payload)
        self.assertNotEqual(result.returncode,0);self.assertFalse(self.assets.exists());self.assertFalse(self.journal.exists())
    def test_extra_traversal_entry_is_refused_before_host_mutation(self):
        result=self.deliver(self.payload(extra='../escaped'))
        self.assertNotEqual(result.returncode,0);self.assertFalse(self.assets.exists());self.assertFalse((self.directory/'escaped').exists())
    def test_active_job_forbids_payload_replacement(self):
        payload=self.payload();self.assertEqual(self.deliver(payload).returncode,0)
        (self.journal/'state.json').write_text('{"phase":"READY"}')
        result=self.deliver(self.payload(changed=True));self.assertNotEqual(result.returncode,0)
        self.assertEqual((self.assets/'.PAYLOAD_SHA256').read_text(),app.hashlib.sha256(payload).hexdigest())


class ActualStagingTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build=tempfile.TemporaryDirectory();cls.tool=Path(cls.build.name)/'release-tool'
        subprocess.run(['go','build','-trimpath','-o',str(cls.tool),'./cmd/release-tool'],cwd=REPO/'backend',check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    @classmethod
    def tearDownClass(cls):cls.build.cleanup()
    def setUp(self):
        from release_prepare_test import ReleaseTest, prepare
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.directory=Path(self.tmp.name)
        self.args=ReleaseTest.fixture(self,self.directory);prepare.prepare(self.args)
        self.assets=self.directory/'assets';self.assets.mkdir()
        config=json.loads(self.args.trust.read_text())
        config.update(manifest_url='https://release.example.invalid/manifest.json',signature_url='https://release.example.invalid/manifest.sig')
        (self.assets/'distribution.json').write_text(json.dumps(config))
        shutil.copyfile(self.tool,self.assets/'release-tool-amd64');(self.assets/'release-tool-amd64').chmod(0o700)
        self.journal=app.Journal(self.directory/'journal')
        self.state=app.Installation(self.journal,self.assets,check=lambda:None).begin('8.8.8.8')
        def download(url,destination,*args):
            source=self.args.output/'manifest.json' if url.endswith('/manifest.json') else self.args.output/'manifest.sig' if url.endswith('/manifest.sig') else self.args.amd64
            shutil.copyfile(source,destination);destination.chmod(0o600)
        for context in [patch.object(app,'UPDATER',self.directory/'watermark'),patch.object(app,'APPLICATION',self.directory/'installed'),
                        patch.object(app.platform,'machine',return_value='x86_64'),patch.object(app,'download',side_effect=download)]:
            context.start();self.addCleanup(context.stop)
        self.operations=app.operations(self.journal,self.assets)
    def test_actual_verifier_accepts_stages_and_commits_configuration(self):
        for phase in ['METADATA','DOWNLOAD','STAGE','CONFIGURE']:self.operations[phase](self.state)
        self.assertEqual(self.state['sequence'],2)
        self.assertTrue((self.directory/'installed/secrets/envelope_key').is_file())
        self.assertTrue((self.directory/'watermark/watermark.json').is_file())
    def test_newer_accepted_release_blocks_old_initial_deployment(self):
        for phase in ['METADATA','DOWNLOAD','STAGE']:self.operations[phase](self.state)
        (self.directory/'watermark/watermark.json').write_text('{"sequence":3,"digest":"'+'a'*64+'"}')
        with self.assertRaises(ValueError):self.operations['CONFIGURE'](self.state)
        self.assertFalse((self.directory/'installed').exists())
    def test_bad_signature_never_reaches_bundle_execution(self):
        (self.args.output/'manifest.sig').write_text(base64.b64encode(bytes(64)).decode())
        with self.assertRaises(subprocess.CalledProcessError):self.operations['METADATA'](self.state)
        self.assertFalse((self.journal.root/'metadata-accepted.json').exists())
        self.assertFalse((self.directory/'installed').exists())
    def test_old_python_cache_is_restaged_by_actual_independent_verifier(self):
        for phase in ['METADATA','DOWNLOAD','STAGE']: self.operations[phase](self.state)
        stage = self.journal.root / 'stage'
        cache = stage / '__pycache__'; cache.mkdir()
        (cache / 'bootstrap.cpython-313.pyc').write_bytes(b'UNTRUSTED CACHE')
        self.operations['CONFIGURE'](self.state)
        self.assertFalse(cache.exists())
        self.assertTrue((self.directory/'installed/secrets/envelope_key').is_file())
    def test_cache_recovery_never_accepts_a_changed_signed_bundle(self):
        for phase in ['METADATA','DOWNLOAD','STAGE']: self.operations[phase](self.state)
        cache = self.journal.root / 'stage/__pycache__'; cache.mkdir()
        (cache / 'bootstrap.cpython-313.pyc').write_bytes(b'UNTRUSTED CACHE')
        bundle = self.journal.root / 'bundle.tar.gz'; bundle.write_bytes(bundle.read_bytes() + b'changed')
        with self.assertRaises(subprocess.CalledProcessError): self.operations['CONFIGURE'](self.state)
        self.assertFalse((self.directory/'installed').exists())

    def test_changed_bundle_and_changed_accepted_stage_are_refused(self):
        self.operations['METADATA'](self.state);self.operations['DOWNLOAD'](self.state)
        bundle=self.journal.root/'bundle.tar.gz';bundle.write_bytes(bundle.read_bytes()+b'changed')
        with self.assertRaises(subprocess.CalledProcessError):self.operations['STAGE'](self.state)
        self.assertFalse((self.journal.root/'stage').exists())
        self.operations['DOWNLOAD'](self.state);self.operations['STAGE'](self.state)
        (self.journal.root/'stage/bootstrap.py').write_text('UNTRUSTED CODE')
        with self.assertRaises(ValueError):self.operations['CONFIGURE'](self.state)
        self.assertFalse((self.directory/'installed').exists())


if __name__=='__main__':unittest.main()
