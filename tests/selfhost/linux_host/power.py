"""Opt-in forced-VM-stop checkpoints; persistent guest disks, normal boot recovery."""
import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import uuid

import guest
import system_disk

CHECKPOINT=guest.FIXTURE/'power-checkpoint.json'


def runtime():
    sys.path.insert(0,str(guest.RUNTIME))
    import updater,updater_host
    from maintenance import durable_json,Fence,DIRECTORY
    from updater_engine import Coordinator
    return updater,updater_host,durable_json,Fence,DIRECTORY,Coordinator


def pause(job_id,case):
    updater,host,write,Fence,DIRECTORY,Coordinator=runtime()
    write(CHECKPOINT,{'id':job_id,'case':case,'boot':Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
                     'totp':guest.db('SELECT last_step FROM owner_mfa')[0][0]})
    os.kill(os.getpid(),signal.SIGSTOP)
    raise ValueError('checkpoint must end by forced VM stop, never resume the fixture child')


def child(job_id,case):
    updater,host,write,Fence,DIRECTORY,Coordinator=runtime()
    copy=host.atomic_copy
    def atomic_copy(source,destination,*args,**kwargs):
        copy(source,destination,*args,**kwargs)
        if case in {'install_mid','disk_full'} and destination==guest.ROOT/'.env':
            if case=='disk_full':
                write(CHECKPOINT,{'id':job_id,'case':case,'boot':Path('/proc/sys/kernel/random/boot_id').read_text().strip(),
                    'totp':guest.db('SELECT last_step FROM owner_mfa')[0][0]})
                system_disk.exhaust()
                os.kill(os.getpid(),signal.SIGSTOP)
                raise ValueError('checkpoint must end by forced VM stop')
            pause(job_id,case)
    host.atomic_copy=atomic_copy
    class TestFence(Fence):
        def release(self,job):
            if case=='committed':pause(job_id,case)
            super().release(job)
    with updater.exclusive():
        Coordinator(guest.JOURNAL/job_id,TestFence(DIRECTORY,wait=55),host.LinuxHost(guest.ROOT,guest.JOURNAL/job_id)).resume()
    raise ValueError('checkpoint not reached')


def arm(case):
    updater,host,write,Fence,DIRECTORY,Coordinator=runtime()
    if CHECKPOINT.exists():raise ValueError('prior checkpoint requires verification')
    # The journal must be on the persistent guest disk, not a prior ENOSPC tmpfs.
    for path in (guest.JOURNAL,guest.ROOT):
        if guest.run('findmnt','-n','-o','FSTYPE','-T',str(path))=='tmpfs':raise ValueError('persistent filesystem required')
    deadline=time.monotonic()+30
    while not guest.db("SELECT EXISTS(SELECT 1 FROM edge_observation_health WHERE julianday('now')-julianday(last_seen_at) BETWEEN 0 AND 2.0/86400)")[0][0]:
        if time.monotonic()>deadline:raise ValueError('fresh observation unavailable')
        time.sleep(.2)
    sequence=json.loads((guest.JOURNAL/'watermark.json').read_text())['sequence']+1
    digest=guest.offer('host-test-new',sequence,8)
    catalog=guest.JOURNAL/'catalog'/digest
    job_id=str(uuid.uuid4())
    with updater.exclusive():
        updater.queue(argparse.Namespace(id=job_id,manifest=catalog/'manifest.json',signature=catalog/'manifest.sig',bundle=catalog/'bundle.tar.gz',architecture='arm64'))
    process=subprocess.Popen([sys.executable,__file__,'child',job_id,case],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)
    deadline=time.monotonic()+180
    while not CHECKPOINT.exists() or (case=='disk_full' and 'State:\tT' not in Path('/proc/'+str(process.pid)+'/status').read_text()):
        if time.monotonic()>deadline:raise ValueError('checkpoint timeout; inspect private stand state')
        time.sleep(.2)
    print('ARMED: '+case,flush=True)


def verify():
    updater,host,write,Fence,DIRECTORY,Coordinator=runtime()
    stamp=json.loads(CHECKPOINT.read_text())
    if stamp['boot']==Path('/proc/sys/kernel/random/boot_id').read_text().strip():raise ValueError('guest did not reboot')
    expected='ROLLED_BACK' if stamp['case'] in {'install_mid','disk_full'} else 'SUCCEEDED'
    # No coordinator invocation here: production boot-enabled systemd must resume.
    job=guest.wait_job(stamp['id'],expected)
    guest.wait_ready('host-test-new');guest.assert_preserved()
    installed=host.LinuxHost(guest.ROOT,guest.FIXTURE/'power-health').installed()
    if installed!=job['old' if expected=='ROLLED_BACK' else 'target']:raise ValueError('version/schema/sequence mismatch')
    if guest.db('SELECT last_step FROM owner_mfa')[0][0]<stamp['totp']:raise ValueError('TOTP watermark rewound')
    if expected=='ROLLED_BACK':
        if not job.get('restored_credentials_revoked'):raise ValueError('restored authentication not revoked')
        for table in ('user_sessions','owner_recovery','auth_enrollment'):
            if guest.db('SELECT count(*) FROM '+table)[0][0]:raise ValueError('restored capability survived')
    write(guest.FIXTURE/('power-'+stamp['id']+'.json'),{'case':stamp['case'],'phase':expected,'boot_changed':True})
    if stamp['case']=='disk_full':
        status=guest.run('systemctl','show','streamtool-updater-space.service','-p','ExecMainStatus','--value')
        released=int(guest.run('systemctl','show','streamtool-updater-space.service','-p','ExecMainExitTimestampMonotonic','--value'))
        docker_started=int(guest.run('systemctl','show','docker.service','-p','ExecMainStartTimestampMonotonic','--value'))
        if status!='0' or not 0 < released <= docker_started:raise ValueError('space release did not precede Docker startup')
        if not system_disk.FILLER.exists() or system_disk.FILLER.stat().st_blocks*512 < (1<<30):raise ValueError('filler removed before boot recovery')
        system_disk.FILLER.unlink() # Cleanup after automatic boot health/data checks.
    CHECKPOINT.unlink()
    print('PASS: forced VM stop '+stamp['case']+' → automatic boot '+expected,flush=True)


if __name__=='__main__':
    guest.guard()
    parser=argparse.ArgumentParser();parser.add_argument('action',choices=('arm','child','verify'))
    parser.add_argument('value',nargs='?');parser.add_argument('case',nargs='?')
    args=parser.parse_args()
    if args.action=='arm':
        if args.value not in ('install_mid','committed','disk_full'):parser.error('unknown checkpoint')
        arm(args.value)
    elif args.action=='child':child(args.value,args.case)
    else:verify()
