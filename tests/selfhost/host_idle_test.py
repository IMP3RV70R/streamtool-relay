"""Real SQLite/fence regression for idle observation before slow verification."""
import hashlib
import json
from pathlib import Path
import sqlite3
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'infra/selfhost'))
import updater_host
from maintenance import Fence,initialize


class IdleOrderingTest(unittest.TestCase):
    def fixture(self,path,stale=False):
        root=path/'root';root.mkdir();(root/'state').mkdir()
        directory=path/'job';directory.mkdir();stage=directory/'stage';stage.mkdir()
        manifest={'version':'fixture','sequence':1,'target_schema':8}
        data=json.dumps(manifest).encode();(directory/'manifest.json').write_bytes(data)
        image='sha256:'+'a'*64
        for name in (*updater_host.HOST_FILES,'compose.yml','Caddyfile','mediamtx.yml'):(stage/name).write_text('fixture')
        (stage/'VERSION').write_text('fixture');(stage/'ARCHITECTURE').write_text({'x86_64':'amd64','aarch64':'arm64','arm64':'arm64'}[updater_host.platform.machine()]);(stage/'worker-image').write_text(image)
        for name in updater_host.IMAGE_KEYS.values():(stage/name).write_text(image)
        (stage/'SHA256SUMS').write_text(''.join(hashlib.sha256(p.read_bytes()).hexdigest()+'  '+p.name+'\n' for p in stage.iterdir()))
        (root/'api.env').write_text('MAINTENANCE_DIRECTORY=/maintenance\nEDGE_ID=fixture-edge\n')
        (root/'node.env').write_text('MAINTENANCE_DIRECTORY=/var/lib/streamtool-updater/admission\n')
        (root/'.env').write_text(''.join(k+'='+image+'\n' for k in updater_host.IMAGE_KEYS))
        with sqlite3.connect(root/'state/control.sqlite') as db:
            db.executescript('CREATE TABLE ingest_connections(status TEXT); CREATE TABLE stream_sessions(phase TEXT); CREATE TABLE worker_allocations(state TEXT); CREATE TABLE edge_observation_health(edge_id TEXT,last_seen_at TEXT);')
            db.execute("INSERT INTO edge_observation_health VALUES('fixture-edge',strftime('%Y-%m-%dT%H:%M:%fZ','now',?))",('-30 seconds' if stale else '0 seconds',))
        old={'version':'fixture','sequence':0,'schema':8,'worker_image':image}
        job={'id':'job','digest':hashlib.sha256(data).hexdigest(),'old':old,'target':{**old,'sequence':1}}
        host=updater_host.LinuxHost(root,directory);host.installed=lambda:old;host.health=lambda *a,**k:None;host.owned_workers=lambda:'';host.compose=lambda *a:'fixture-container'
        return host,job

    def check(self,stale):
        with tempfile.TemporaryDirectory() as temporary:
            path=Path(temporary);host,job=self.fixture(path,stale)
            admission=path/'admission';initialize(admission);verified=[]
            def command(*args,**kwargs):
                if args[0].endswith('/release-tool'):
                    verified.append(True)
                    with sqlite3.connect(host.root/'state/control.sqlite') as db:db.execute("UPDATE edge_observation_health SET last_seen_at=strftime('%Y-%m-%dT%H:%M:%fZ','now','-30 seconds')")
                    return 'verified'
                if args[:2]==('docker','inspect'):return json.dumps([{'Source':'/var/lib/streamtool-updater/admission','Destination':'/maintenance','RW':False}])
                if 'config' in args:return json.dumps({'name':'streamtool-selfhost','services':{'api':{},'edge':{},'proxy':{}}})
                if args[:3]==('docker','image','inspect'):return '0'
                raise AssertionError('unexpected fixture command')
            with patch.object(updater_host.LinuxHost,'reserve'),patch.object(updater_host,'command',command),patch.object(updater_host.shutil,'disk_usage',return_value=SimpleNamespace(free=10<<30)):
                with Fence(admission).hold(job['id']):
                    if stale:
                        with self.assertRaisesRegex(ValueError,'missing or stale publisher observation'):host.idle(job)
                        self.assertFalse(verified)
                    else:
                        host.idle(job);self.assertEqual(verified,[True])
                    Fence(admission).release(job['id'])

    def test_fresh_observation_does_not_expire_during_fenced_verification(self):self.check(False)
    def test_initially_stale_observation_fails_before_verification(self):self.check(True)

if __name__=='__main__':unittest.main()
