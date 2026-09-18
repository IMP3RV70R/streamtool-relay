"""Opt-in real root-filesystem exhaustion; guarded disposable guest only."""
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

FILLER=guest.FIXTURE/'system-disk-filler'


def exhaust():
    # Use real allocation, including ext4's root reserve; sparse truncate is not
    # filesystem exhaustion. Leave a small tail for actual ENOSPC writes.
    stats=os.statvfs(FILLER.parent)
    with FILLER.open('xb',buffering=0) as output:
        os.fchmod(output.fileno(),0o600)
        length=max(0,stats.f_bfree*stats.f_frsize-(1<<20))
        try:
            os.posix_fallocate(output.fileno(),0,length)
            output.seek(length)
            while True:output.write(bytes(65536))
        except OSError as error:
            if error.errno!=errno.ENOSPC:raise
        os.fsync(output.fileno())


def child(job_id,case,automatic=False):
    updater,host,backup,Fence,DIRECTORY,Coordinator=matrix.runtime()
    import updater_engine
    original_copy=host.atomic_copy
    filled=False
    def atomic_copy(source,destination,*args,**kwargs):
        nonlocal filled
        original_copy(source,destination,*args,**kwargs)
        if case=='install_mid' and destination==guest.ROOT/'.env' and not filled:
            filled=True;exhaust()
    host.atomic_copy=atomic_copy
    original_durable=updater_engine.durable_json
    def durable(path,value):
        nonlocal filled
        if case=='terminal' and value.get('phase')=='SUCCEEDED' and not filled:
            if (DIRECTORY/'active.json').exists():raise ValueError('post-commit admission not open')
            with sqlite3.connect(guest.ROOT/'state/control.sqlite') as database:
                database.execute('PRAGMA synchronous=FULL')
                database.execute('INSERT INTO host_acceptance_probe VALUES(?)',('post-root-disk-'+job_id,))
            filled=True;exhaust()
        return original_durable(path,value)
    updater_engine.durable_json=durable
    try:
        if automatic:
            sys.argv=['updater.py','resume']
            updater.main()
            return
        with updater.exclusive():
            Coordinator(guest.JOURNAL/job_id,Fence(DIRECTORY,wait=55),host.LinuxHost(guest.ROOT,guest.JOURNAL/job_id)).resume()
    except OSError as error:
        if filled and error.errno==errno.ENOSPC:sys.exit(98)
        raise
    raise ValueError('real full-filesystem checkpoint not reached')


def execute(case,automatic=False):
    updater,host,backup,Fence,DIRECTORY,Coordinator=matrix.runtime()
    if FILLER.exists():raise ValueError('prior fixture filler requires administrator inspection')
    for path in (guest.JOURNAL,guest.ROOT,Path('/var/lib/docker')):
        if os.stat(path).st_dev!=os.stat('/').st_dev:raise ValueError('single actual root filesystem required')
    if guest.run('findmnt','-n','-o','FSTYPE','-T','/')!='ext4':raise ValueError('fixture ext4 root required')
    sequence=json.loads((guest.JOURNAL/'watermark.json').read_text())['sequence']+1
    digest=guest.offer('host-test-new',sequence,8);catalog=guest.JOURNAL/'catalog'/digest
    job_id=str(uuid.uuid4())
    with updater.exclusive():
        job=updater.queue(argparse.Namespace(id=job_id,manifest=catalog/'manifest.json',signature=catalog/'manifest.sig',bundle=catalog/'bundle.tar.gz',architecture='arm64'))
    deadline=time.monotonic()+30
    while not guest.db("SELECT EXISTS(SELECT 1 FROM edge_observation_health WHERE julianday('now')-julianday(last_seen_at) BETWEEN 0 AND 2.0/86400)")[0][0]:
        if time.monotonic()>deadline:raise ValueError('fresh fixture observation unavailable')
        time.sleep(.2)
    step=guest.db('SELECT last_step FROM owner_mfa')[0][0]
    try:
        command=[sys.executable,__file__,'--child',job_id,'--case',case]
        if automatic:command.append('--automatic')
        result=subprocess.run(command,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,timeout=240)
        if result.returncode!=(0 if automatic else 98):raise ValueError('expected actual ENOSPC not observed')
        current=json.loads((guest.JOURNAL/job_id/'job.json').read_text())
        expected_prior=('ROLLED_BACK' if case=='install_mid' else 'SUCCEEDED') if automatic else ('INSTALLING' if case=='install_mid' else 'COMMITTED')
        if current['phase']!=expected_prior:raise ValueError('unexpected full-disk journal phase')
        if (DIRECTORY/'active.json').exists()!=((case=='install_mid') and not automatic):raise ValueError('wrong full-disk admission state')
        # The child returned ENOSPC from the real transaction on this root
        # filesystem. Docker/log cleanup can free blocks before this supervisor
        # observes them, so do not infer absence of exhaustion from later statvfs.
    finally:
        # Explicit environmental capacity repair; only our fixed private filler.
        # Keep this supervisor alive while full, never reboot during this test.
        if not automatic:
            if FILLER.exists():FILLER.unlink()
            guest.run('systemctl','reset-failed','streamtool-updater.service')
            guest.run('systemctl','start','--no-block','streamtool-updater.service')
    expected='ROLLED_BACK' if case=='install_mid' else 'SUCCEEDED'
    final=guest.wait_job(job_id,expected)
    guest.wait_ready('host-test-new');guest.assert_preserved()
    installed=host.LinuxHost(guest.ROOT,guest.FIXTURE/'system-disk-health').installed()
    if installed!=job['old' if case=='install_mid' else 'target']:raise ValueError('matching installation not restored')
    if guest.db('SELECT last_step FROM owner_mfa')[0][0]<step:raise ValueError('TOTP watermark rewound')
    if expected=='ROLLED_BACK':
        if not final.get('restored_credentials_revoked'):raise ValueError('restored authentication revocation absent')
        for table in ('user_sessions','owner_recovery','auth_enrollment'):
            if guest.db('SELECT count(*) FROM '+table)[0][0]:raise ValueError('restored capability survived')
    if case=='terminal':
        if guest.db('SELECT count(*) FROM host_acceptance_probe WHERE value=?',('post-root-disk-'+job_id,))[0][0]!=1:raise ValueError('post-admission mutation rewound')
        with sqlite3.connect(guest.ROOT/'state/control.sqlite') as database:
            database.execute('DELETE FROM host_acceptance_probe WHERE value=?',('post-root-disk-'+job_id,))
    guest.private_json(guest.FIXTURE/('system-disk-'+job_id+'.json'),{'case':case,'enospc':True,'prior':expected_prior,'phase':expected,'capacity_repaired':True})
    if automatic:
        if not FILLER.exists() or FILLER.stat().st_blocks*512 < (1<<30):raise ValueError('filler removed before automatic recovery')
        inventory=json.loads((guest.JOURNAL/job_id/'reserve.json').read_text())
        if any(Path(entry['path']).exists() for entry in inventory['entries']):raise ValueError('reserve not released')
        FILLER.unlink() # Test cleanup only, after terminal health/data verification.
    print('PASS: actual root-disk ENOSPC '+case+' → '+expected+(' via automatic reserve release' if automatic else ' after explicit capacity repair'),flush=True)
    # Only this verified terminal fixture's duplicate artifacts, never user data.
    for path in (guest.JOURNAL/job_id).iterdir():
        if path.is_dir():shutil.rmtree(path)
    shutil.rmtree(catalog);(guest.JOURNAL/'candidate.json').unlink()


if __name__=='__main__':
    guest.guard()
    parser=argparse.ArgumentParser();parser.add_argument('--case',choices=('install_mid','terminal'),required=True);parser.add_argument('--child');parser.add_argument('--automatic',action='store_true')
    args=parser.parse_args()
    try:
        if args.child:child(args.child,args.case,args.automatic)
        else:execute(args.case,args.automatic)
    except Exception as error:
        if not args.child and FILLER.exists():FILLER.unlink()
        codes={'single actual root filesystem required':'filesystem_layout', 'fixture ext4 root required':'filesystem_type', 'expected actual ENOSPC not observed':'checkpoint_exit', 'unexpected full-disk journal phase':'journal_phase', 'wrong full-disk admission state':'admission_state', 'actual root filesystem not full':'capacity_observation', 'fresh fixture observation unavailable':'observation', 'prior fixture filler requires administrator inspection':'existing_filler'}
        code=codes.get(str(error),'unclassified') if isinstance(error,ValueError) else type(error).__name__
        print('FAIL: system disk acceptance '+code,file=sys.stderr);sys.exit(1)
