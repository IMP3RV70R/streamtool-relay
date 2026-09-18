#!/usr/bin/env python3
"""Autonomous first installation from APK-pinned trust and verifier binaries."""
import base64
import errno
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import time
import urllib.request
from urllib.parse import urlsplit
import uuid

# The signed staged inventory must stay immutable across retries.
sys.dont_write_bytecode = True

import installer
from deploy import InitialDeployment, durable_tree

ROOT = Path('/var/lib/streamtool-installer/application')
ASSETS = Path('/usr/local/libexec/streamtool-installer')
APPLICATION = Path('/opt/streamtool')
UPDATER = Path('/var/lib/streamtool-updater/journal')
PHASES = ['PREPARE','METADATA','DOWNLOAD','STAGE','IMAGES','CONFIGURE','HOST','START','READY']
UNIT_PATH = Path('/etc/systemd/system/streamtool-installation.service')
TIMER_PATH = Path('/etc/systemd/system/streamtool-installation.timer')
UNIT = '''[Unit]
Description=streamtool-relay initial installation
After=network-online.target docker.service
Wants=network-online.target
StartLimitIntervalSec=2700
StartLimitBurst=3
[Service]
Type=oneshot
ExecStart=/usr/bin/python3 /usr/local/libexec/streamtool-installer/installer_application.py run
TimeoutStartSec=45min
Restart=on-failure
RestartSec=20
UMask=0077
[Install]
WantedBy=multi-user.target
'''
TIMER = '''[Unit]
Description=Resume streamtool-relay initial installation
[Timer]
OnActiveSec=10
OnUnitInactiveSec=30
Unit=streamtool-installation.service
[Install]
WantedBy=timers.target
'''


def digest(path):
    with path.open('rb') as file: return hashlib.file_digest(file,'sha256').hexdigest()


def https(value):
    parsed = urlsplit(value)
    if (parsed.scheme != 'https' or not parsed.hostname or parsed.username or parsed.password
        or parsed.fragment or parsed.query or parsed.port not in (None,443)):
        raise ValueError('invalid distribution URL')
    return parsed


def configuration(path):
    data = path.read_bytes()
    if len(data)>4096: raise ValueError('oversized distribution configuration')
    config = json.loads(data)
    if set(config) != {'public_key','channel','artifact_origin','manifest_url','signature_url'}:
        raise ValueError('distribution not configured')
    if len(base64.b64decode(config['public_key'],validate=True)) != 32 or config['channel'] not in ['stable','preview']:
        raise ValueError('invalid distribution trust')
    origin = https(config['artifact_origin'])
    if origin.path: raise ValueError('origin must not have a path')
    for name in ['manifest_url','signature_url']:
        url = https(config[name])
        if url.netloc != origin.netloc: raise ValueError('foreign metadata URL')
    return config


class Redirects(urllib.request.HTTPRedirectHandler):
    def __init__(self, origin):
        parsed = urlsplit(origin)
        self.origin = parsed.netloc
        self.github = parsed.hostname == 'github.com' and parsed.port in (None,443)
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        url = urlsplit(newurl)
        # GitHub Releases uses an expiring CDN redirect; its query is transport
        # metadata only, never persisted or reported. Signed bytes remain mandatory.
        permitted = url.netloc == self.origin or self.github and url.hostname == 'release-assets.githubusercontent.com' and url.port in (None,443)
        if not permitted or url.scheme != 'https' or url.username or url.password or url.fragment:
            raise ValueError('unapproved download redirect')
        return super().redirect_request(req,fp,code,msg,headers,newurl)


def download(url, destination, maximum, origin, expected_size=None, expected_hash=None):
    parsed = https(url)
    if parsed.netloc != urlsplit(origin).netloc: raise ValueError('foreign artifact URL')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), Redirects(origin))
    temporary = destination.with_name('.download-' + destination.name)
    if temporary.is_symlink() or destination.is_symlink(): raise ValueError('unsafe download path')
    try:
        stamp = hashlib.sha256(); size = 0; deadline = time.monotonic()+900
        with opener.open(urllib.request.Request(url,headers={'User-Agent':'streamtool-relay-installer/1'}),timeout=30) as response, temporary.open('wb') as output:
            if response.status != 200: raise ValueError('download refused')
            os.fchmod(output.fileno(),0o600)
            while True:
                if time.monotonic() >= deadline: raise TimeoutError()
                data = response.read(128*1024)
                if not data: break
                size += len(data)
                if size > maximum: raise ValueError('download too large')
                stamp.update(data); output.write(data)
            if expected_size is not None and (size != expected_size or stamp.hexdigest() != expected_hash):
                raise ValueError('artifact mismatch')
            output.flush(); os.fsync(output.fileno())
        os.replace(temporary,destination)
        from deploy import sync
        sync(destination.parent)
    finally: temporary.unlink(missing_ok=True)


class Journal(installer.Journal):
    def read(self):
        try: fd = os.open(self.root/'state.json',os.O_RDONLY|os.O_NOFOLLOW)
        except FileNotFoundError: return None
        with os.fdopen(fd,'rb') as file: data=file.read(8193)
        if len(data)>8192: raise ValueError('oversized journal')
        state = json.loads(data)
        if (state.get('protocol') != 1 or state.get('phase') not in PHASES+['PENDING','SUCCEEDED','FAILED']
            or type(state.get('attempts')) is not int or not 0 <= state['attempts'] <= 3
            or type(state.get('deadline')) is not int or state.get('resume_phase') not in PHASES+['PENDING']
            or str(uuid.UUID(state['job_id'])) != state['job_id'] or state.get('directory') != str(self.root)
            or not re.fullmatch(r'[0-9a-f]{64}',state.get('distribution_digest',''))):
            raise ValueError('invalid installation journal')
        if public_ip(state['address']) != state['address']: raise ValueError('noncanonical IP')
        return state


def public_ip(value):
    address = ipaddress.ip_address(value)
    if not address.is_global or address.is_reserved or address.is_multicast or getattr(address,'ipv4_mapped',None) or '%' in value:
        raise ValueError('public IP required')
    return str(address)


def conflicts():
    installer.host_checks()
    for directory in ['/var/lib/streamtool-updater','/usr/local/libexec/streamtool-updater','/usr/local/libexec/streamtool-maintenance']:
        if Path(directory).exists(): raise ValueError('existing host runtime')
    for target, _ in InitialDeployment.destinations().values():
        if target.exists() or target.is_symlink(): raise ValueError('existing host file')
    for kind in ['passwd','group']:
        if subprocess.run(['getent',kind,'streamtool-agent'],stdout=subprocess.DEVNULL).returncode == 0:
            raise ValueError('existing host identity')
    if shutil.which('docker'): network_check()


def network_check(owned=False):
    ids = subprocess.check_output(['docker','network','ls','-q'],text=True).split()
    if not ids: return
    networks = json.loads(subprocess.check_output(['docker','network','inspect',*ids]))
    desired = [ipaddress.ip_network(s) for s in ['172.30.80.0/24','172.30.81.0/24','172.30.82.0/29']]
    for network in networks:
        names = dict(zip(['streamtool-control','streamtool-media','streamtool-authorization'],desired))
        if network.get('Name') in names:
            wanted = names[network['Name']]
            configs = (network.get('IPAM') or {}).get('Config') or []
            if owned and (network.get('Labels') or {}).get('com.docker.compose.project') == 'streamtool-selfhost' and len(configs)==1 and configs[0].get('Subnet')==str(wanted):
                continue
            raise ValueError('existing network name')
        for item in (network.get('IPAM') or {}).get('Config') or []:
            subnet = item.get('Subnet')
            if subnet and any(ipaddress.ip_network(subnet).overlaps(wanted) for wanted in desired): raise ValueError('subnet conflict')


def failure_details(error):
    # Exception text, argv, paths and provider output may contain credentials.
    details = {}
    code = 'installation_failed'
    if isinstance(error, ValueError):
        code = {'changed accepted stage': 'stage_inventory_changed', 'image platform mismatch': 'image_platform_mismatch',
                'insufficient staging space': 'staging_space_insufficient'}.get(str(error), code)
    if isinstance(error, subprocess.TimeoutExpired): code = 'command_timeout'
    elif isinstance(error, TimeoutError): code = 'installation_timeout'
    elif isinstance(error, OSError) and error.errno == errno.ENOSPC: code = 'disk_full'
    elif isinstance(error, subprocess.CalledProcessError): code = 'command_failed'
    if isinstance(error, (subprocess.CalledProcessError, subprocess.TimeoutExpired)):
        args = error.cmd
        name = Path(args[0]).name if isinstance(args, (list, tuple)) and args and isinstance(args[0], str) else ''
        if name in ('docker', 'systemctl', 'apt-get', 'release-tool-amd64', 'release-tool-arm64'):
            details['command'] = name
        if isinstance(error, subprocess.CalledProcessError): details['exit_code'] = error.returncode
    if isinstance(error, OSError) and error.errno is not None: details['errno'] = error.errno
    return code, details


class Installation:
    def __init__(self, journal, assets=ASSETS, check=conflicts):
        self.journal,self.assets,self.check = journal,assets,check
    def begin(self,address):
        address = public_ip(address)
        config = configuration(self.assets/'distribution.json')
        pin = digest(self.assets/'distribution.json')
        with self.journal.lock():
            state = self.journal.read()
            if state:
                if state['address'] != address or state['distribution_digest'] != pin: raise ValueError('different installation request')
                if state['phase'] != 'FAILED': return state
                # Retry only the durable failed phase, never initialize a second owner.
                state.update(phase=state['resume_phase'], attempts=0,deadline=int(time.time())+2700,error=None,failure=None)
            else:
                self.check()
                state = {'protocol':1,'job_id':str(uuid.uuid4()),'phase':'PENDING','resume_phase':'PENDING',
                         'attempts':0,'deadline':int(time.time())+2700,'address':address,'directory':str(self.journal.root),
                         'distribution_digest':pin,'error':None}
            trust = {key:config[key] for key in ['public_key','channel','artifact_origin']}
            installer.atomic(self.journal.root/'trust.json',(json.dumps(trust)+'\n').encode())
            self.journal.write(state)
            return state
    def run(self,operations):
        with self.journal.lock():
            state = self.journal.read()
            if not state or state['phase'] in ['SUCCEEDED','FAILED']: return state
            if state['attempts'] >= 3 or time.time() >= state['deadline']:
                state.update(phase='FAILED',error='recovery_budget_exhausted'); self.journal.write(state); return state
            state['attempts'] += 1; self.journal.write(state)
            try:
                if digest(self.assets/'distribution.json') != state['distribution_digest']: raise ValueError('changed trust')
                if callable(operations): operations = operations()
                start = 0 if state['phase']=='PENDING' else PHASES.index(state['phase'])
                for phase in PHASES[start:]:
                    if time.time() >= state['deadline']: raise TimeoutError()
                    state.update(phase=phase,resume_phase=phase,error=None,failure=None); self.journal.write(state)
                    operations[phase](state)
                    self.journal.write(state)
                state.update(phase='SUCCEEDED',error=None); self.journal.write(state)
            except Exception as error:
                state['error'], state['failure'] = failure_details(error)
                if state['attempts'] >= 3: state['phase']='FAILED'
                self.journal.write(state); raise
            return state


def cache_only_change(expected, actual):
    extra = set(actual) - set(expected)
    return (bool(extra) and all(actual.get(name) == stamp for name, stamp in expected.items())
            and all(re.fullmatch(r'(?:[A-Za-z0-9_-]+/)*__pycache__/[A-Za-z_][A-Za-z0-9_]*\.cpython-[0-9]{2,3}(?:\.opt-[12])?\.pyc', name) for name in extra))


def operations(journal,assets=ASSETS):
    root = journal.root; config = configuration(assets/'distribution.json')
    architecture = {'x86_64':'amd64','aarch64':'arm64'}[platform.machine()]
    verifier = str(assets/('release-tool-'+architecture))
    def verify(metadata=False):
        UPDATER.mkdir(mode=0o700,parents=True,exist_ok=True)
        args = [verifier,'verify','--trust',str(root/'trust.json'),'--state',str(UPDATER/'watermark.json'),
                '--manifest',str(root/'manifest.json'),'--signature',str(root/'manifest.sig'),'--architecture',architecture,'--initial']
        if metadata: args += ['--metadata-only']
        else: args += ['--bundle',str(root/'bundle.tar.gz'),'--destination',str(root/'stage')]
        installer.command(*args,timeout=900)
    def metadata(state):
        stamp = root/'metadata-accepted.json'
        if not stamp.exists():
            download(config['manifest_url'],root/'manifest.json',65536,config['artifact_origin'])
            download(config['signature_url'],root/'manifest.sig',256,config['artifact_origin'])
            verify(True)
            installer.atomic(stamp,json.dumps({'digest':digest(root/'manifest.json')}).encode())
        if json.loads(stamp.read_text())['digest'] != digest(root/'manifest.json'): raise ValueError('changed metadata')
        manifest = json.loads((root/'manifest.json').read_bytes())
        state.update(version=manifest['version'],sequence=manifest['sequence'],schema=manifest['target_schema'])
    def artifact():
        return next(a for a in json.loads((root/'manifest.json').read_bytes())['artifacts'] if a['architecture']==architecture)
    def bundle(state):
        item = artifact(); path = root/'bundle.tar.gz'
        if path.exists() and path.stat().st_size==item['size'] and digest(path)==item['sha256']: return
        if shutil.disk_usage(root).free < item['size']*3+(4<<30): raise ValueError('insufficient staging space')
        download(item['url'],path,item['size'],config['artifact_origin'],item['size'],item['sha256'])
    def stage(state):
        ready = root/'stage-accepted.json'
        if ready.exists():
            if json.loads(ready.read_text())['digest'] != digest(root/'manifest.json'): raise ValueError('changed stage identity')
            path = root/'stage'
            if path.is_symlink() or any(p.is_symlink() for p in path.rglob('*')): raise ValueError('unsafe accepted stage')
            actual = {p.relative_to(path).as_posix():digest(p) for p in path.rglob('*') if p.is_file()}
            expected = json.loads(ready.read_text())['inventory']
            if actual == expected: return
            if not cache_only_change(expected, actual): raise ValueError('changed accepted stage')
            # Restore only the private job's staging from the independently verified
            # signed bundle. Never accept altered files by changing their hashes.
            ready.unlink(); durable_tree(root)
        path = root/'stage'
        if path.is_symlink(): raise ValueError('unsafe stage')
        if path.exists(): shutil.rmtree(path)  # Only this private job's incomplete stage.
        verify(False); durable_tree(path)
        inventory = {p.relative_to(path).as_posix():digest(p) for p in path.rglob('*') if p.is_file()}
        installer.atomic(ready,json.dumps({'digest':digest(root/'manifest.json'),'inventory':inventory}).encode())
    def prepare(state):
        preparation = installer.Preparation(installer.Journal(installer.ROOT))
        job = preparation.begin()
        if job['phase'] != 'PREPARED': preparation.run(installer.steps(job['docker_managed']))
        installer.host_checks(); network_check()
    checked_stage = False
    def integrity():
        nonlocal checked_stage
        accepted = json.loads((root/'stage-accepted.json').read_text())
        path = root/'stage'
        if path.is_symlink() or any(p.is_symlink() for p in path.rglob('*')): raise ValueError('unsafe accepted stage')
        if accepted['digest'] != digest(root/'manifest.json'): raise ValueError('changed release identity')
        watermark = json.loads((UPDATER/'watermark.json').read_text())
        manifest = json.loads((root/'manifest.json').read_text())
        if watermark['sequence'] != manifest['sequence'] or watermark['digest'] != accepted['digest']:
            raise ValueError('superseded installation release')
        if checked_stage: return
        actual = {p.relative_to(path).as_posix():digest(p) for p in path.rglob('*') if p.is_file()}
        if actual != accepted['inventory']:
            stage({})
            restored = json.loads((root/'stage-accepted.json').read_text())
            actual = {p.relative_to(path).as_posix():digest(p) for p in path.rglob('*') if p.is_file()}
            if actual != restored['inventory']: raise ValueError('changed accepted stage')
        checked_stage = True
    def deployment(method):
        def action(state):
            integrity()
            getattr(InitialDeployment(APPLICATION,root/'stage',state),method)()
        return action
    return {'PREPARE':prepare,'METADATA':metadata,'DOWNLOAD':bundle,'STAGE':stage,
            **{phase:deployment(method) for phase,method in [('IMAGES','image_load'),('CONFIGURE','configure'),('HOST','host'),('START','start'),('READY','ready')]}}


def main():
    if os.geteuid()!=0 or sys.platform!='linux' or len(sys.argv)!=2 or sys.argv[1] not in ['install','status','run']:
        raise ValueError('invalid invocation')
    journal = Journal(ROOT); installation = Installation(journal)
    action = sys.argv[1]
    if action=='status':
        state = journal.read() or {'protocol':1,'phase':'NOT_STARTED'}
    elif action=='install':
        data = sys.stdin.buffer.read(1025)
        request = json.loads(data)
        if len(data)>1024 or set(request) != {'address'}: raise ValueError('invalid request')
        # Validate trust/host before registering wake; wake precedes committing PENDING.
        configuration(ASSETS/'distribution.json'); address = public_ip(request['address'])
        existing = journal.read()
        if existing:
            if existing['address'] != address or existing['distribution_digest'] != digest(ASSETS/'distribution.json'):
                raise ValueError('different installation request')
            if existing['phase']=='SUCCEEDED':
                print(json.dumps({key:existing[key] for key in ['protocol','job_id','phase','address','version'] if key in existing})); return
        else: conflicts()
        for path, body in [(UNIT_PATH,UNIT),(TIMER_PATH,TIMER)]:
            if path.is_symlink() or path.exists() and path.read_text()!=body: raise ValueError('foreign service')
            installer.atomic(path,body.encode(),0o644)
        installer.command('systemctl','daemon-reload',timeout=30)
        installer.command('systemctl','enable','streamtool-installation.service',timeout=30)
        installer.command('systemctl','enable','--now','streamtool-installation.timer',timeout=30)
        state = installation.begin(request['address'])
        if state['phase']!='SUCCEEDED':
            installer.command('systemctl','reset-failed','streamtool-installation.service',timeout=30)
            installer.command('systemctl','start','--no-block','streamtool-installation.service',timeout=30)
    else:
        state = installation.run(lambda: operations(journal))
        if state and state['phase'] in ['SUCCEEDED','FAILED']:
            installer.command('systemctl','disable','--now','streamtool-installation.timer',timeout=30)
    print(json.dumps({key:state[key] for key in ['protocol','job_id','phase','resume_phase','attempts','address','version','error','failure'] if key in state} if state else {'protocol':1,'phase':'NOT_STARTED'}))


if __name__=='__main__':
    try: main()
    except Exception:
        print('{"protocol":1,"error":"installation_refused"}',file=sys.stderr); sys.exit(1)
