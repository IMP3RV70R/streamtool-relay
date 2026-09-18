#!/usr/bin/env python3
"""Offline backup/restore. Always preserve the current installation before restore."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import subprocess
import tarfile
import tempfile
import time


def run(*args):
    return subprocess.check_output(args,text=True,stderr=subprocess.STDOUT).strip()


def compose(root,*args):
    return run('docker','compose','--project-directory',str(root),'-f',str(root/'compose.yml'),*args)


@contextlib.contextmanager
def exclusive(root):
    descriptor=os.open(root/'state/control.sqlite.lock',os.O_RDWR|os.O_CREAT|os.O_NOFOLLOW,0o600)
    with os.fdopen(descriptor,'r+b') as lock:
        if not stat.S_ISREG(os.fstat(lock.fileno()).st_mode): raise ValueError('invalid database lock file')
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        yield


def snapshot(root,output):
    if output.exists(): raise ValueError('backup destination already exists')
    output.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
    output.touch(mode=0o600,exist_ok=False)
    try:
        with exclusive(root), tempfile.TemporaryDirectory(dir=output.parent, prefix='.snapshot-') as temporary:
            staging=Path(temporary)
            source=sqlite3.connect((root/'state/control.sqlite').as_uri()+'?mode=ro',uri=True)
            target=sqlite3.connect(staging/'control.sqlite')
            try:
                source.backup(target)
                if target.execute('PRAGMA integrity_check').fetchone()[0]!='ok': raise ValueError('database integrity check failed')
            finally: target.close();source.close()
            for name in ('secrets','certs'):
                shutil.copytree(root/name,staging/name)
            for name in ('keyring.json','media-node.json','api.env','.env','node.env','worker-network.env'):
                shutil.copyfile(root/name,staging/name)
            fence=Path('/var/lib/streamtool/fence.json')
            if fence.exists(): shutil.copyfile(fence,staging/'fence.json')
            manifest={str(p.relative_to(staging)):hashlib.file_digest(p.open('rb'),'sha256').hexdigest() for p in staging.rglob('*') if p.is_file()}
            (staging/'manifest.json').write_text(json.dumps(manifest))
            with tarfile.open(output,'w:gz') as archive:
                for p in sorted(staging.iterdir()): archive.add(p,arcname=p.name)
        descriptor=os.open(output,os.O_RDONLY)
        try: os.fsync(descriptor)
        finally: os.close(descriptor)
    except BaseException:
        output.unlink(missing_ok=True);raise


def revoke_restored_auth(database):
    # Backup cannot know which credentials were consumed/revoked afterwards.
    # Preserve the authenticator and encryption keys, but never resurrect sessions,
    # enrollment capabilities or single-use recovery codes.
    tables={row[0] for row in database.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    for table in ('user_sessions','auth_enrollment','owner_recovery'):
        if table in tables: database.execute('DELETE FROM '+table)
    if 'update_authorizations' in tables:
        database.execute("UPDATE update_authorizations SET state='CANCELLED' WHERE state='PENDING'")
    if 'owner_mfa' in tables:
        # An old snapshot can also rewind the consumed TOTP counter. Fence every
        # currently acceptable step, including the adjacent future window.
        database.execute('UPDATE owner_mfa SET last_step=max(last_step,?)',(int(time.time())//30+1,))


def restore(root,backup):
    workers=run('docker','ps','-aq','--filter','label=streamtool.node=00000000-0000-4000-8000-000000000001')
    if workers: raise ValueError('remove all installation workers before restore; see the restore runbook')
    if compose(root,'ps','-q'): raise ValueError('stop all installation services before restore')
    if subprocess.run(['systemctl','is-active','--quiet','streamtool-agent']).returncode==0:
        raise ValueError('stop the Agent before restore')
    previous=root.parent/('streamtool-before-restore-'+str(time.time_ns())+'.tar.gz')
    snapshot(root,previous)
    restore_snapshot(root,backup)
    print('Restored. Previous installation backup: '+str(previous))


def restore_snapshot(root,backup):
    with exclusive(root),tempfile.TemporaryDirectory(dir=root, prefix='.restore-') as temporary:
        staging=Path(temporary)
        with tarfile.open(backup,'r:gz') as archive: archive.extractall(staging,filter='data')
        manifest=json.loads((staging/'manifest.json').read_text())
        actual={str(p.relative_to(staging)):hashlib.file_digest(p.open('rb'),'sha256').hexdigest() for p in staging.rglob('*') if p.is_file() and p.name!='manifest.json'}
        if actual!=manifest: raise ValueError('backup checksum mismatch')
        database=sqlite3.connect(staging/'control.sqlite')
        try:
            if database.execute('PRAGMA integrity_check').fetchone()[0]!='ok': raise ValueError('backup database integrity check failed')
            # A restored snapshot must never resurrect a broadcast which may have
            # ended after the backup. Preserve history and require a new publisher.
            database.execute("UPDATE stream_sessions SET desired='STOPPED',phase='ENDED',operator_stopped=1,end_reason='MANUAL_RESTORE',ended_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE phase NOT IN ('ENDED','FAILED')")
            database.execute("UPDATE worker_allocations SET desired='STOPPED',state='STOPPED' WHERE state NOT IN ('STOPPED','FAILED')")
            database.execute("UPDATE ingest_connections SET status='DISCONNECTED',disconnected_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE status='CONNECTED'")
            database.execute('DELETE FROM edge_observation_health')
            revoke_restored_auth(database)
            database.commit()
        finally: database.close()
        # Keep a newer Agent high watermark; the controller recovers from it.
        fence=Path('/var/lib/streamtool/fence.json')
        old=int(fence.read_text()) if fence.exists() else 0
        new=int((staging/'fence.json').read_text()) if (staging/'fence.json').exists() else 0
        for name in ('secrets','certs'):
            olddir=root/(name+'.before-'+str(time.time_ns()))
            if (root/name).exists(): (root/name).rename(olddir)
            shutil.copytree(staging/name,root/name)
        for name in ('keyring.json','media-node.json','api.env','.env','node.env','worker-network.env'):
            shutil.copyfile(staging/name,root/name)
        shutil.copyfile(staging/'control.sqlite',root/'state/control.sqlite.restore')
        with (root/'state/control.sqlite.restore').open('rb') as restored: os.fsync(restored.fileno())
        os.replace(root/'state/control.sqlite.restore',root/'state/control.sqlite')
        for suffix in ('-wal','-shm'): (root/('state/control.sqlite'+suffix)).unlink(missing_ok=True)
        fence.parent.mkdir(parents=True,exist_ok=True)
        temporary=fence.with_name('fence.json.restore')
        with temporary.open('w') as output:
            output.write(str(max(old,new))+'\n');output.flush();os.fsync(output.fileno())
        os.replace(temporary,fence)
        shutil.copyfile(root/'node.env','/etc/streamtool/node.env')
        shutil.copyfile(root/'worker-network.env','/etc/streamtool/worker-network.env')
        normalize(root)
        # Persist restored files and directory entries before health/commit can
        # open admission. The coordinator may resume this restoration after reboot.
        from maintenance import sync_directory
        for base in (root/'secrets',root/'certs',root/'state'):
            for path in base.rglob('*'):
                if path.is_file():
                    with path.open('rb') as restored: os.fsync(restored.fileno())
            for path in sorted((p for p in base.rglob('*') if p.is_dir()),reverse=True): sync_directory(path)
            sync_directory(base)
        for name in ('keyring.json','media-node.json','api.env','.env','node.env','worker-network.env'):
            with (root/name).open('rb') as restored: os.fsync(restored.fileno())
        for path in (fence,Path('/etc/streamtool/node.env'),Path('/etc/streamtool/worker-network.env')):
            with path.open('rb') as restored: os.fsync(restored.fileno())
            sync_directory(path.parent)
        sync_directory(root)


def normalize(root):
    # Called by update/restore; never rotate or replace key material.
    import grp,pwd
    agent=pwd.getpwnam('streamtool-agent');group=grp.getgrnam('streamtool-agent').gr_gid
    os.chown(root,0,group);root.chmod(0o750)
    for directory in (root/'state',root/'secrets'):
        os.chown(directory,65532,65532);directory.chmod(0o700)
    for p in (root/'secrets').iterdir(): os.chown(p,65532,65532);p.chmod(0o600)
    for name in ('control.sqlite','control.sqlite.lock'):
        p=root/'state'/name
        if p.exists(): os.chown(p,65532,65532);p.chmod(0o600)
    for name in ('keyring.json','media-node.json'):
        os.chown(root/name,65532,65532);(root/name).chmod(0o600)
    certs=root/'certs';os.chown(certs,0,group);certs.chmod(0o750)
    for p in certs.iterdir(): os.chown(p,0,0);p.chmod(0o600)
    for p in certs.glob('*.crt'): p.chmod(0o644)
    for name in ('api','controller'): os.chown(certs/(name+'.key'),65532,65532)
    os.chown(certs/'agent.key',0,group);(certs/'agent.key').chmod(0o640)
    fence=Path('/var/lib/streamtool/fence.json')
    if fence.exists(): os.chown(fence,agent.pw_uid,group);fence.chmod(0o600)
    for p in (Path('/etc/streamtool/node.env'),Path('/etc/streamtool/worker-network.env')): p.chmod(0o600)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('action',choices=('backup','restore','permissions'))
    parser.add_argument('path',type=Path,nargs='?');parser.add_argument('--root',type=Path,default=Path('/opt/streamtool'))
    args=parser.parse_args()
    if os.geteuid()!=0: raise SystemExit('Run as root.')
    root=args.root.resolve()
    from maintenance import operation
    with operation():
        if args.action=='permissions': normalize(root)
        elif args.path is None: parser.error('backup/restore path required')
        elif args.action=='restore': restore(root,args.path.resolve())
        else:
            compose(root,'stop','api')
            try: snapshot(root,args.path.resolve());print('Backup created: '+str(args.path))
            finally: compose(root,'start','api')
