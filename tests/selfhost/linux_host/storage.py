"""Opt-in real filesystem exhaustion and journal-write boundaries on own VM only."""
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
import matrix


def fill(path):
    with path.open('xb') as output:
        try:
            while True:output.write(bytes(1<<20));output.flush()
        except OSError as error:
            if error.errno!=errno.ENOSPC:raise


def primitives():
    guest.guard()
    updater,host,backup,Fence,DIRECTORY,Coordinator=matrix.runtime()
    import maintenance
    fixture=guest.FIXTURE/('storage-'+str(uuid.uuid4()));fixture.mkdir(mode=0o700)
    volume=fixture/'volume';volume.mkdir()
    host.command('mount','-t','tmpfs','-o','size=65536,mode=0700','streamtool-test-storage',str(volume))
    try:
        state=volume/'state.json';maintenance.durable_json(state,{'phase':'old'})
        fill(volume/'filler')
        try:maintenance.durable_json(state,{'phase':'new'})
        except OSError as error:
            if error.errno!=errno.ENOSPC:raise
        else:raise ValueError('journal write unexpectedly succeeded on full filesystem')
        if json.loads(state.read_text())!={'phase':'old'}:raise ValueError('ENOSPC destroyed previous journal')
        if list(volume.glob('.journal-*')):raise ValueError('failed write leaked journal temporary')
        (volume/'filler').unlink();maintenance.durable_json(state,{'phase':'new'})
        print('PASS: real ENOSPC preserves complete old JSON and permits retry after capacity returns',flush=True)
        root=fixture/'site';(root/'web').mkdir(parents=True);(root/'web/index.html').write_text('old')
        source=fixture/'source';source.mkdir();(source/'index.html').write_text('new')
        try:host.LinuxHost(root,volume).replace_web(source,'previous-web')
        except OSError as error:
            if error.errno!=errno.EXDEV:raise
            print('FINDING: real cross-filesystem web replacement refused with EXDEV',flush=True)
            return False
        if (root/'web/index.html').read_text()!='new':raise ValueError('cross-filesystem replacement lost website')
        print('PASS: actual web replacement with journal on a different filesystem',flush=True)
        return True
    finally:
        host.command('umount',str(volume));shutil.rmtree(fixture)


def child(job_id,point,mode):
    guest.guard()
    updater,host,backup,Fence,DIRECTORY,Coordinator=matrix.runtime()
    import updater_engine
    directory=guest.JOURNAL/job_id
    underlying=guest.FIXTURE/('storage-job-'+job_id)
    def mount_volume():
        directory.rename(underlying);directory.mkdir(mode=0o700)
        host.command('mount','-t','tmpfs','-o','size=1073741824,mode=0700','streamtool-test-journal',str(directory))
        for name in ('job.json','manifest.json','manifest.sig','reserve.json'):shutil.copyfile(underlying/name,directory/name)
        shutil.copytree(underlying/'planned',directory/'planned')
        (directory/'stage').mkdir();host.command('mount','--bind',str(underlying/'stage'),str(directory/'stage'))
    class Driver(host.LinuxHost):
        def idle(self,job):
            super().idle(job)
            mount_volume() # Capacity loss after real headroom/idle validation.
        def health(self,job,old):
            if not old and point in {'ROLLING_BACK','ROLLBACK_VERIFYING','RECOVERED'}:raise ValueError('injected target health refusal')
            super().health(job,old)
    original_durable=updater_engine.durable_json
    def durable(path,value):
        if value.get('phase')==point:
            if mode=='full':
                fill(directory/'filler')
                guest.private_json(underlying/'fault-observed.json',{'point':point,'mode':mode})
                return original_durable(path,value) # Actual ENOSPC; no pretend error.
            if mode=='before':os._exit(97)
            original_durable(path,value)
            os._exit(97)
        return original_durable(path,value)
    updater_engine.durable_json=durable
    with updater.exclusive():
        Coordinator(directory,Fence(DIRECTORY,wait=55),Driver(guest.ROOT,directory)).resume()


def transaction(point,mode):
    guest.guard()
    updater,host,backup,Fence,DIRECTORY,Coordinator=matrix.runtime()
    sequence=json.loads((guest.JOURNAL/'watermark.json').read_text())['sequence']+1
    digest=guest.offer('host-test-new',sequence,8);catalog=guest.JOURNAL/'catalog'/digest
    job_id=str(uuid.uuid4());directory=guest.JOURNAL/job_id;underlying=guest.FIXTURE/('storage-job-'+job_id)
    with updater.exclusive():
        job=updater.queue(argparse.Namespace(id=job_id,manifest=catalog/'manifest.json',signature=catalog/'manifest.sig',bundle=catalog/'bundle.tar.gz',architecture='arm64'))
    deadline=time.monotonic()+30
    while not guest.db("SELECT EXISTS(SELECT 1 FROM edge_observation_health WHERE julianday('now')-julianday(last_seen_at) BETWEEN 0 AND 2.0/86400)")[0][0]:
        if time.monotonic()>deadline:raise ValueError('fixture fresh observation unavailable')
        time.sleep(.2)
    result=subprocess.run([sys.executable,__file__,'transaction','--child',job_id,'--point',point,'--mode',mode],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    if result.returncode!=(1 if mode=='full' else 97):raise ValueError('storage fault did not reach expected checkpoint')
    if mode=='full':
        if not (underlying/'fault-observed.json').exists():raise ValueError('full journal checkpoint not observed')
        if not (DIRECTORY/'active.json').exists() and point!='SUCCEEDED':raise ValueError('journal failure lost admission fence')
        json.loads((directory/'job.json').read_text()) # Must remain complete even while full.
        (directory/'filler').unlink()
    if point=='SUCCEEDED':
        if (DIRECTORY/'active.json').exists():raise ValueError('terminal write checkpoint admission not open')
        with sqlite3.connect(guest.ROOT/'state/control.sqlite') as database:database.execute("INSERT INTO host_acceptance_probe VALUES('post-storage-commit mutation')")
    restored_step=guest.db('SELECT last_step FROM owner_mfa')[0][0]
    current=json.loads((directory/'job.json').read_text())
    with updater.exclusive():final=updater.coordinator(job_id).resume()
    target=current['phase'] in {'COMMITTED','SUCCEEDED'}
    expected_phase='SUCCEEDED' if target else ('FAILED' if current['phase'] in {'QUEUED','PREPARING','ABORTING'} else 'ROLLED_BACK')
    if final['phase']!=expected_phase:raise ValueError('unexpected storage recovery phase')
    guest.wait_ready('host-test-new');guest.assert_preserved()
    expected=job['target'] if target else job['old']
    if host.LinuxHost(guest.ROOT,guest.FIXTURE/'storage-health').installed()!=expected:raise ValueError('storage recovery version/schema mismatch')
    if final['phase']=='ROLLED_BACK':
        if not final.get('restored_credentials_revoked'):raise ValueError('storage recovery auth revocation absent')
        for table in ('user_sessions','owner_recovery','auth_enrollment'):
            if guest.db('SELECT count(*) FROM '+table)[0][0]:raise ValueError('storage recovery revived authentication')
    if guest.db('SELECT last_step FROM owner_mfa')[0][0]<restored_step:raise ValueError('storage rollback rewound TOTP counter')
    if point=='SUCCEEDED':
        if guest.db("SELECT count(*) FROM host_acceptance_probe WHERE value='post-storage-commit mutation'")[0][0]!=1:raise ValueError('storage failure rewound post-commit mutation')
        with sqlite3.connect(guest.ROOT/'state/control.sqlite') as database:database.execute("DELETE FROM host_acceptance_probe WHERE value='post-storage-commit mutation'")
    host.DiskReserve(directory).release(force=True)
    print('PASS: real journal '+point+' '+mode+' → '+final['phase'],flush=True)
    # Preserve a terminal journal on the original backing filesystem before unmount.
    shutil.copyfile(directory/'job.json',underlying/'job.json')
    host.command('umount',str(directory/'stage'));host.command('umount',str(directory));directory.rmdir();underlying.rename(directory)
    for path in directory.iterdir():
        if path.is_dir():shutil.rmtree(path)
    shutil.rmtree(catalog);(guest.JOURNAL/'candidate.json').unlink()


def main():
    guest.guard()
    parser=argparse.ArgumentParser();parser.add_argument('action',choices=('primitives','transaction'));parser.add_argument('--child');parser.add_argument('--point',default='VERIFYING');parser.add_argument('--mode',choices=('full','before','after'),default='full');args=parser.parse_args()
    if args.child:child(args.child,args.point,args.mode)
    elif args.action=='primitives':
        if not primitives():sys.exit(2)
    else:transaction(args.point,args.mode)

if __name__=='__main__':
    try:main()
    except Exception as error:
        print('FAIL: storage acceptance '+type(error).__name__,file=sys.stderr);sys.exit(1)
