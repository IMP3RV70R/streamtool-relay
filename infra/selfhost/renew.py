#!/usr/bin/env python3
"""Renew private TLS leaves during an idle window; never rotate keys or the CA."""
import argparse
from pathlib import Path
import subprocess
import tempfile
import shutil
import sys

from backup import normalize


def run(*args): return subprocess.check_output(args,text=True,stderr=subprocess.STDOUT).strip()


def expires(path,seconds):
    return subprocess.run(['openssl','x509','-checkend',str(seconds),'-noout','-in',str(path)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode!=0


def renew(root,force=False):
    certs=root/'certs'
    leaves={'api':('streamtool-api','DNS:api,IP:172.30.82.2','serverAuth'),
            'edge':('streamtool-edge','DNS:edge','serverAuth'),
            'agent':('streamtool-agent','IP:172.30.80.1','serverAuth'),
            'controller':('streamtool-controller',None,'clientAuth')}
    needed=[name for name in leaves if expires(certs/(name+'.crt'),30*86400)]
    if not needed and not force: print('Private certificates are current.');return
    if expires(certs/'ca.crt',400*86400): raise ValueError('private CA renewal required; do not issue leaves beyond CA validity')
    workers=run('docker','ps','-q','--filter','label=streamtool.node=00000000-0000-4000-8000-000000000001')
    if workers and not force:
        if any(expires(certs/(name+'.crt'),7*86400) for name in needed): raise ValueError('private certificate expires within 7 days; schedule maintenance renewal')
        print('Private renewal deferred until the broadcast is idle.');return
    if workers: raise ValueError('stop/remove workers before forced renewal')
    import contextlib,sqlite3
    with contextlib.closing(sqlite3.connect((root/'state/control.sqlite').as_uri()+'?mode=ro',uri=True)) as database:
        if database.execute("SELECT EXISTS(SELECT 1 FROM ingest_connections WHERE status='CONNECTED') OR EXISTS(SELECT 1 FROM stream_sessions WHERE phase NOT IN ('ENDED','FAILED'))").fetchone()[0]:
            raise ValueError('source or session active')
        observed=database.execute("SELECT EXISTS(SELECT 1 FROM edge_observation_health WHERE julianday('now')-julianday(last_seen_at) BETWEEN 0 AND 6.0/86400)").fetchone()[0]
        if not observed: raise ValueError('publisher observation unavailable')
    with tempfile.TemporaryDirectory(dir=certs) as temporary:
        staging=Path(temporary)
        for name,(cn,san,usage) in leaves.items():
            csr=staging/(name+'.csr');extension=staging/(name+'.ext');certificate=staging/(name+'.crt')
            extension.write_text('extendedKeyUsage='+usage+'\n'+('subjectAltName='+san+'\n' if san else ''))
            run('openssl','req','-new','-key',str(certs/(name+'.key')),'-out',str(csr),'-subj','/CN='+cn)
            run('openssl','x509','-req','-in',str(csr),'-CA',str(certs/'ca.crt'),'-CAkey',str(certs/'ca.key'),'-CAcreateserial','-out',str(certificate),'-days','365','-extfile',str(extension))
            run('openssl','verify','-CAfile',str(certs/'ca.crt'),str(certificate))
        for name in leaves: shutil.copyfile(staging/(name+'.crt'),certs/(name+'.crt.new'))
    for name in leaves: (certs/(name+'.crt.new')).replace(certs/(name+'.crt'))
    normalize(root)
    compose=['docker','compose','--project-directory',str(root),'-f',str(root/'compose.yml')]
    run(*compose,'restart','api','edge')
    run('systemctl','restart','streamtool-agent')
    print('Private leaf certificates renewed; private CA and keys retained.')


if __name__=='__main__':
    parser=argparse.ArgumentParser();parser.add_argument('--root',type=Path,default=Path('/opt/streamtool'));parser.add_argument('--force',action='store_true');args=parser.parse_args()
    from maintenance import operation
    with operation():
        renew(args.root.resolve(),args.force)
