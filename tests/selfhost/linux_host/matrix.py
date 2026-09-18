"""Opt-in root fault injection against real LinuxHost; never installed in runtime."""
import argparse
import errno
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import time
import uuid

import guest

def runtime():
    sys.path.insert(0,str(guest.RUNTIME))
    import updater,updater_host,backup
    from maintenance import Fence,DIRECTORY
    from updater_engine import Coordinator
    return updater,updater_host,backup,Fence,DIRECTORY,Coordinator

CASES={'prepare_after':'FAILED','install_after':'ROLLED_BACK','verify_before':'ROLLED_BACK',
       'restore_after':'ROLLED_BACK','rollback_verify_before':'ROLLED_BACK',
       'commit_before_release':'SUCCEEDED','commit_after_release':'SUCCEEDED',
       'backup_full':'FAILED','load_failure':'ROLLED_BACK','config_failure':'ROLLED_BACK','prepare_stop':'FAILED','install_mid':'ROLLED_BACK','restore_mid':'ROLLED_BACK','rollback_failure':'RECOVERY_REQUIRED'}
CRASH={'prepare_after','install_after','verify_before','restore_after','rollback_verify_before','commit_before_release','commit_after_release','prepare_stop','install_mid','restore_mid'}


def injected(job_id,case):
    updater,host,backup,Fence,DIRECTORY,Coordinator=runtime()
    directory=guest.JOURNAL/job_id
    original_command=host.command
    original_snapshot=backup.snapshot
    def snapshot(root,output):
        if case=='prepare_stop':os._exit(97)
        if case!='backup_full':return original_snapshot(root,output)
        original_command('mount','-t','tmpfs','-o','size=4096,mode=0700','streamtool-test-enospc',str(output.parent))
        try:
            try:original_snapshot(root,output)
            except OSError as error:
                if error.errno!=errno.ENOSPC:raise
                guest.private_json(directory/'enospc-observed.json',{'enospc':True})
                raise
            raise ValueError('fixture backup unexpectedly fit on full filesystem')
        finally:original_command('umount',str(output.parent))
    backup.snapshot=snapshot
    original_restore_snapshot=backup.restore_snapshot
    def restore_snapshot(root,archive):
        original_restore_snapshot(root,archive)
        if case=='restore_mid':os._exit(97)
    backup.restore_snapshot=restore_snapshot
    original_atomic_copy=host.atomic_copy
    def atomic_copy(source,destination,*args,**kwargs):
        original_atomic_copy(source,destination,*args,**kwargs)
        if case=='install_mid' and destination==guest.ROOT/'.env':os._exit(97)
    host.atomic_copy=atomic_copy
    def command(*args,**kwargs):
        if case=='load_failure' and args[:3]==('docker','load','-i') and args[3]==str(directory/'stage/images.tar'):
            return original_command('docker','load','-i','/dev/null',**kwargs)
        if case=='rollback_failure' and args[:3]==('docker','load','-i') and args[3]==str(directory/'recovery/images.tar'):
            return original_command('docker','load','-i','/dev/null',**kwargs)
        return original_command(*args,**kwargs)
    host.command=command
    class Driver(host.LinuxHost):
        restoring=False
        def prepare(self,job):
            super().prepare(job)
            if case=='prepare_after':os._exit(97)
        def install(self,job):
            super().install(job)
            if case=='install_after':os._exit(97)
        def start(self):
            if case=='config_failure' and not self.restoring:
                (self.root/'compose.yml').write_text('services: [\n')
            super().start()
        def health(self,job,old):
            if not old and case in {'restore_after','rollback_verify_before','restore_mid','rollback_failure'}:raise ValueError('injected target health refusal')
            if not old and case=='verify_before':os._exit(97)
            if old and self.restoring and case=='rollback_verify_before':os._exit(97)
            super().health(job,old)
        def restore(self,job):
            self.restoring=True
            super().restore(job)
            if case=='restore_after':os._exit(97)
    class TestFence(Fence):
        def release(self,job):
            if case=='commit_before_release':os._exit(97)
            super().release(job)
            if case=='commit_after_release':os._exit(97)
    with updater.exclusive():
        Coordinator(directory,TestFence(DIRECTORY,wait=55),Driver(guest.ROOT,directory)).resume()


def execute(case):
    updater,host,backup,Fence,DIRECTORY,Coordinator=runtime()
    deadline=time.monotonic()+30
    while True:
        fresh=guest.db("SELECT EXISTS(SELECT 1 FROM edge_observation_health WHERE julianday('now')-julianday(last_seen_at) BETWEEN 0 AND 2.0/86400)")[0][0]
        if fresh:break
        if time.monotonic()>deadline:raise ValueError('fixture publisher observation unavailable')
        time.sleep(.2)
    watermark=json.loads((guest.JOURNAL/'watermark.json').read_text())
    sequence=watermark['sequence']+1
    digest=guest.offer('host-test-new',sequence,8)
    catalog=guest.JOURNAL/'catalog'/digest
    job_id=str(uuid.uuid4())
    with updater.exclusive():
        job=updater.queue(argparse.Namespace(id=job_id,manifest=catalog/'manifest.json',signature=catalog/'manifest.sig',bundle=catalog/'bundle.tar.gz',architecture='arm64'))
    result=subprocess.run([sys.executable,__file__,'--child',job_id,case],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    if result.returncode!=(97 if case in CRASH else 0):raise ValueError('unexpected fault child exit for '+case)
    if case=='commit_after_release':
        if (DIRECTORY/'active.json').exists():raise ValueError('admission not released before checkpoint')
        with sqlite3.connect(guest.ROOT/'state/control.sqlite') as database:
            database.execute("INSERT INTO host_acceptance_probe VALUES('post-commit mutation')")
    restored_step=guest.db('SELECT last_step FROM owner_mfa')[0][0]
    with updater.exclusive():final=updater.coordinator(job_id).resume()
    if final['phase']!=CASES[case]:raise ValueError('unexpected terminal phase for '+case+': '+final['phase'])
    if case=='rollback_failure':
        if not (DIRECTORY/'active.json').exists():raise ValueError('failed recovery reopened admission')
        budget=final['rollback_resumes']
        with updater.exclusive():again=updater.coordinator(job_id).resume()
        if again['phase']!='RECOVERY_REQUIRED' or again['rollback_resumes']!=budget:raise ValueError('failed rollback retried unboundedly')
        print('PASS: real Linux failed rollback stays fenced and terminal without retries',flush=True)
        # Explicit fixture administrator repair after removing the injected fault.
        # This is NOT automatic recovery and NOT a public recovery CLI.
        with updater.exclusive():
            coordinator=updater.coordinator(job_id);coordinator.phase(final,'ROLLING_BACK');final=coordinator.resume()
        if final['phase']!='ROLLED_BACK':raise ValueError('fixture administrator repair failed')
    guest.wait_ready('host-test-new');guest.assert_preserved()
    if guest.db('SELECT last_step FROM owner_mfa')[0][0]<restored_step:raise ValueError('restoration rewound TOTP watermark')
    expected=job['target'] if final['phase']=='SUCCEEDED' else job['old']
    if host.LinuxHost(guest.ROOT,guest.FIXTURE/'matrix-health').installed()!=expected:raise ValueError('matching version/schema/sequence failed for '+case)
    if final['phase']=='ROLLED_BACK':
        if not final.get('restored_credentials_revoked'):raise ValueError('restored auth revocation absent')
        for table in ('user_sessions','owner_recovery','auth_enrollment'):
            if guest.db('SELECT count(*) FROM '+table)[0][0]:raise ValueError('restored credential survived')
    if case=='backup_full' and not (guest.JOURNAL/job_id/'enospc-observed.json').exists():raise ValueError('actual ENOSPC not observed')
    if case=='commit_after_release':
        if guest.db("SELECT count(*) FROM host_acceptance_probe WHERE value='post-commit mutation'")[0][0]!=1:raise ValueError('committed mutation was rewound')
        with sqlite3.connect(guest.ROOT/'state/control.sqlite') as database:database.execute("DELETE FROM host_acceptance_probe WHERE value='post-commit mutation'")
    print('PASS: real Linux '+case+' → '+final['phase']+(' (explicit fixture administrator repair)' if case=='rollback_failure' else ''),flush=True)
    host.DiskReserve(guest.JOURNAL/job_id).release(force=True)
    # Retain the durable journal/evidence but remove only our verified terminal
    # fixture artifacts containing duplicate images/keys, to bound test disk use.
    for path in (guest.JOURNAL/job_id).iterdir():
        if path.is_dir():shutil.rmtree(path)
    shutil.rmtree(catalog)
    (guest.JOURNAL/'candidate.json').unlink()


def false_health():
    updater,host,backup,Fence,DIRECTORY,Coordinator=runtime()
    driver=host.LinuxHost(guest.ROOT,guest.FIXTURE/'false-health')
    version=guest.ROOT/'VERSION';original=version.read_bytes()
    with updater.exclusive():
        with Fence(DIRECTORY).hold('fixture-false-health'):
            try:
                # The real HTTPS API returns 200 while its version disagrees with
                # metadata. Accelerate only the retry timer after this real probe.
                version.write_text('incorrect-fixture-version\n')
                expected=driver.installed()
                probe=driver.private_json(('172.30.82.2',8080),'/v1/installation',token=(guest.ROOT/'secrets/admin_token').read_text().strip())
                if probe['version']!='host-test-new' or probe['version']==expected['version']:raise ValueError('HTTP-200 version mismatch fixture unavailable')
                original_monotonic=host.time.monotonic
                calls=[]
                def monotonic():
                    calls.append(True)
                    return 0 if len(calls)==1 else 100
                host.time.monotonic=monotonic
                try:
                    try:driver.health({'old':expected},old=True)
                    except ValueError as error:
                        if str(error)!='application readiness failed':raise
                    else:raise ValueError('false positive readiness was accepted')
                finally:host.time.monotonic=original_monotonic
            finally:
                version.write_bytes(original)
                Fence(DIRECTORY).release('fixture-false-health')
    guest.wait_ready('host-test-new');guest.assert_preserved()
    print('PASS: real Linux HTTP-200 API with incorrect version rejected by host readiness',flush=True)


def main():
    guest.guard()
    parser=argparse.ArgumentParser();parser.add_argument('--child');parser.add_argument('cases',nargs='+',choices=(*CASES,'false_health'));args=parser.parse_args()
    if args.child:injected(args.child,args.cases[0])
    else:
        for case in args.cases:
            if case=='false_health':false_health()
            else:execute(case)

if __name__=='__main__':
    try:main()
    except Exception as error:
        print('FAULT CLASS: '+type(error).__name__,file=sys.stderr)
        print('FAIL: real Linux fault matrix; inspect fixed journal/status locally',file=sys.stderr);sys.exit(1)
