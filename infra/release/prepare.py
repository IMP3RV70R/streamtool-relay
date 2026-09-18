"""Prepare/verify signed offline release metadata; never publish or generate keys."""
import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
from urllib.parse import urlsplit


def prepare(args):
    args.tool=args.tool.resolve(strict=True)
    if not args.tool.is_file():raise ValueError('trusted tool must be a regular file')
    repository=Path(__file__).resolve().parents[2]
    key=args.key.resolve(strict=True)
    if key.is_relative_to(repository) or key.is_relative_to(args.output.resolve()):raise ValueError('signing seed must remain outside repository/output')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]{0,63}',args.version):raise ValueError('invalid version')
    if not 0 < args.sequence < 2**64 or not 0 <= args.minimum_sequence < args.sequence:raise ValueError('invalid sequence bounds')
    if not 0 <= args.minimum_schema <= args.maximum_schema <= args.target_schema:raise ValueError('invalid schema bounds')
    if not 1 <= args.valid_days <= 31:raise ValueError('invalid validity')
    base=urlsplit(args.base_url)
    if base.scheme!='https' or not base.hostname or base.username or base.password or base.query or base.fragment:raise ValueError('fixed HTTPS release base required')
    origin='https://'+base.netloc
    trust=json.loads(args.trust.read_text())
    if trust['artifact_origin']!=origin or trust['channel'] not in ('stable','preview'):raise ValueError('trust does not match release origin/channel')
    notes=args.notes.read_text() if args.notes else ''
    if len(notes.encode())>8192:raise ValueError('notes too large')
    artifacts=[]
    for architecture,path in (('amd64',args.amd64),('arm64',args.arm64)):
        if path.is_symlink() or not path.is_file():raise ValueError('regular bundle required')
        size=path.stat().st_size
        if not 0 < size <= 8<<30:raise ValueError('invalid bundle size')
        expected='streamtool-relay-'+args.version+'-'+architecture+'.tar.gz'
        if path.name!=expected:raise ValueError('bundle filename must bind version and architecture')
        with path.open('rb') as incoming:stamp=hashlib.file_digest(incoming,'sha256').hexdigest()
        artifacts.append({'architecture':architecture,'url':args.base_url.rstrip('/')+'/'+expected,'size':size,'sha256':stamp})
    now=datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
    timestamp=lambda value:value.isoformat().replace('+00:00','Z')
    manifest={'target_schema':args.target_schema,'sequence':args.sequence,'version':args.version,'channel':trust['channel'],
        'created':timestamp(now),'expires':timestamp(now+datetime.timedelta(days=args.valid_days)), 'protocol':1,
        'minimum_sequence':args.minimum_sequence,'minimum_schema':args.minimum_schema,'maximum_schema':args.maximum_schema,
        'notes':notes,'artifacts':artifacts}
    data=(json.dumps(manifest,sort_keys=True,separators=(',',':'))+'\n').encode()
    if len(data)>65536:raise ValueError('manifest too large')
    # The caller provides an independently built trusted tool, public trust and
    # external signing seed. Secrets are never copied into output or printed.
    args.output.mkdir(mode=0o700)
    try:
        manifest_path=args.output/'manifest.json';manifest_path.write_bytes(data)
        signature=args.output/'manifest.sig'
        subprocess.run([str(args.tool),'sign','--key',str(args.key),'--manifest',str(manifest_path),'--signature',str(signature)],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        with tempfile.TemporaryDirectory(prefix='.release-verification-',dir=args.output) as temporary:
            private=Path(temporary)
            for architecture,bundle in (('amd64',args.amd64),('arm64',args.arm64)):
                subprocess.run([str(args.tool),'verify','--trust',str(args.trust),'--state',str(private/'watermark.json'),
                    '--manifest',str(manifest_path),'--signature',str(signature),'--bundle',str(bundle),
                    '--destination',str(private/architecture),'--architecture',architecture,
                    '--installed-sequence',str(args.minimum_sequence),'--installed-schema',str(args.minimum_schema),'--initial'],
                    check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
                shutil.rmtree(private/architecture)
        # Verification checked bytes copied from each signed bundle. Rehash original
        # files before handing metadata to the publisher to reject changed inputs.
        for artifact,bundle in zip(artifacts,(args.amd64,args.arm64)):
            with bundle.open('rb') as incoming:stamp=hashlib.file_digest(incoming,'sha256').hexdigest()
            if stamp!=artifact['sha256'] or bundle.stat().st_size!=artifact['size']:raise ValueError('bundle changed during preparation')
        for path in (manifest_path,signature):
            with path.open('rb') as incoming:os.fsync(incoming.fileno())
        descriptor=os.open(args.output,os.O_RDONLY|os.O_DIRECTORY)
        try:os.fsync(descriptor)
        finally:os.close(descriptor)
    except BaseException:
        shutil.rmtree(args.output)
        raise


def main():
    parser=argparse.ArgumentParser()
    for name in ('tool','key','trust','amd64','arm64','output'):
        parser.add_argument('--'+name,type=Path,required=True)
    for name in ('version','base-url'):parser.add_argument('--'+name,required=True)
    for name in ('sequence','minimum-sequence','minimum-schema','maximum-schema','target-schema'):
        parser.add_argument('--'+name,type=int,required=True)
    parser.add_argument('--valid-days',type=int,default=7);parser.add_argument('--notes',type=Path)
    args=parser.parse_args()
    try:prepare(args)
    except Exception:
        parser.exit(1,'Release preparation failed; no publication performed.\n')
    print('PASS: both signed bundles verified offline; no publication performed')

if __name__=='__main__':main()
