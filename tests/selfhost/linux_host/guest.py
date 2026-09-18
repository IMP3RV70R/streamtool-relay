"""Real installer/Docker/systemd/TLS/SQLite acceptance on the disposable Lima VM.
Never run on a user server. TLS and signing keys are isolated fixture credentials.
"""
import argparse
import base64
import contextlib
import hashlib
import hmac
import http.client
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import ssl
import struct
import subprocess
import sys
import time
import uuid

FIXTURE=Path('/var/lib/streamtool-host-acceptance')
PAYLOAD=Path('/root/streamtool-host-acceptance')
ROOT=Path('/opt/streamtool')
JOURNAL=Path('/var/lib/streamtool-updater/journal')
RUNTIME=Path('/usr/local/libexec/streamtool-updater')
DOMAIN='relay.acceptance.test'


def guard():
    if os.geteuid()!=0 or sys.platform!='linux' or socket.gethostname()!='lima-streamtool-update' or not Path('/run/systemd/system').exists():
        raise ValueError('disposable Lima acceptance guest required')


def run(*arguments):
    return subprocess.check_output(arguments,stderr=subprocess.DEVNULL,text=True).strip()


def private_json(path,data):
    path.write_text(json.dumps(data));path.chmod(0o600)


def db(query,parameters=()):
    with contextlib.closing(sqlite3.connect(ROOT/'state/control.sqlite')) as database:
        return database.execute(query,parameters).fetchall()


def fingerprint():
    digest=hashlib.sha256()
    for query in ('SELECT id,account_id,password_hash,password_salt FROM users ORDER BY id',
                  'SELECT user_id,secret FROM owner_mfa ORDER BY user_id',
                  'SELECT * FROM account_sources ORDER BY account_id',
                  'SELECT id,account_id,name,ingest_key_hash,enabled FROM streams ORDER BY id',
                  'SELECT id,stream_id,name,endpoint,secret_ciphertext,secret_nonce,key_id,enabled FROM destinations ORDER BY id'):
        for row in db(query):
            digest.update(repr(row).encode())
    digest.update((ROOT/'secrets/envelope_key').read_bytes())
    digest.update((ROOT/'certs/ca.key').read_bytes())
    return digest.hexdigest()


class Owner:
    def __init__(self):
        self.cookie=''
        self.credentials=json.loads((FIXTURE/'owner.json').read_text()) if (FIXTURE/'owner.json').exists() else None

    def request(self,method,path,body=None,expect=200,setup=False):
        connection=http.client.HTTPSConnection(DOMAIN,443,timeout=10)
        headers={'X-Streamtool':'1','Content-Type':'application/json','Origin':'https://'+DOMAIN}
        if self.cookie: headers['Cookie']=self.cookie
        if setup: headers['X-Setup-Token']=(ROOT/'secrets/setup_token').read_text().strip()
        try:
            connection.request(method,path,body=json.dumps(body) if body is not None else None,headers=headers)
            response=connection.getresponse();data=response.read(1<<20)
            if response.status!=expect: raise ValueError(f'owner API {method} {path} unexpected HTTP {response.status}')
            for key,value in response.getheaders():
                if key.lower()=='set-cookie' and value.startswith('streamtool_session='):self.cookie=value.split(';')[0]
            return json.loads(data) if data else None
        finally: connection.close()

    def fresh_code(self):
        while time.time()/30 <= db('SELECT last_step FROM owner_mfa')[0][0]+1:
            time.sleep(1)
        step=int(time.time())//30
        secret=base64.b32decode(self.credentials['secret']+'='*((-len(self.credentials['secret']))%8))
        digest=hmac.new(secret,struct.pack('>Q',step),hashlib.sha1).digest();offset=digest[-1]&15
        return str((struct.unpack('>I',digest[offset:offset+4])[0]&0x7fffffff)%1000000).zfill(6)

    def enroll(self):
        password=secrets.token_urlsafe(24)
        challenge=self.request('POST','/v1/auth/setup',{'password':password},setup=True)
        self.credentials={'password':password,'secret':challenge['secret']}
        # The pending owner has no owner_mfa row until confirmation.
        secret=base64.b32decode(self.credentials['secret']+'='*((-len(self.credentials['secret']))%8))
        digest=hmac.new(secret,struct.pack('>Q',int(time.time())//30),hashlib.sha1).digest();offset=digest[-1]&15
        code=str((struct.unpack('>I',digest[offset:offset+4])[0]&0x7fffffff)%1000000).zfill(6)
        self.request('POST','/v1/auth/setup/confirm',{'enrollment_token':challenge['enrollment_token'],'code':code})
        private_json(FIXTURE/'owner.json',self.credentials)
        self.request('POST','/v1/me/source',{})
        # Disabled public-IP fixture output never transmits to a third party.
        self.request('POST','/v1/me/source/outputs',{'name':'preserved fixture','endpoint':'rtmp://93.184.216.34/live','secret':secrets.token_urlsafe(24),'enabled':False},expect=201)

    def login(self):
        # Failed fixture preparation can consume legitimate login budgets. Respect
        # their durable expiry instead of clearing or weakening host protection.
        key=hashlib.sha256(b'auth:owner').hexdigest()
        while True:
            rows=db("SELECT count,(julianday(expires_at)-julianday('now'))*86400 FROM auth_attempts WHERE key=?",(key,))
            if not rows or rows[0][0]<9 or rows[0][1]<=0:break
            time.sleep(1)
        self.request('POST','/v1/auth/login',{'password':self.credentials['password'],'code':self.fresh_code()})


def wait_ready(version,timeout=150):
    sys.path.insert(0,str(RUNTIME))
    from updater_host import LinuxHost
    host=LinuxHost(ROOT,FIXTURE/'health')
    deadline=time.time()+timeout
    while time.time()<deadline:
        try:
            expected=host.installed()
            if expected['version']!=version: raise ValueError('version not yet installed')
            host.health({'old':expected},old=True)
            return
        except Exception:
            time.sleep(2)
    raise ValueError('actual installer health did not converge')


def initialize(retry=False):
    if (ROOT/'api.env').exists() or (FIXTURE.exists() and not retry):raise ValueError('refusing existing installation')
    FIXTURE.mkdir(mode=0o700,exist_ok=retry);tls=FIXTURE/'tls';tls.mkdir(exist_ok=retry)
    run('systemctl','enable','--now','docker')
    run('docker','compose','version')
    run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(tls/'ca.key'),'-out',str(tls/'ca.crt'),'-days','3','-subj','/CN=streamtool-host-acceptance','-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign')
    run('openssl','req','-newkey','rsa:2048','-nodes','-keyout',str(tls/'relay.key'),'-out',str(tls/'relay.csr'),'-subj','/CN='+DOMAIN)
    (tls/'extensions').write_text('subjectAltName=DNS:'+DOMAIN+'\nextendedKeyUsage=serverAuth\n')
    run('openssl','x509','-req','-in',str(tls/'relay.csr'),'-CA',str(tls/'ca.crt'),'-CAkey',str(tls/'ca.key'),'-CAcreateserial','-out',str(tls/'relay.crt'),'-days','3','-extfile',str(tls/'extensions'))
    for name in ('ca.key','relay.key'): (tls/name).chmod(0o600)
    run('cp',str(tls/'ca.crt'),'/usr/local/share/ca-certificates/streamtool-host-acceptance.crt');run('update-ca-certificates')
    with Path('/etc/hosts').open('a') as output:output.write('\n127.0.0.1 '+DOMAIN+'\n')
    log=FIXTURE/'install.log'
    with log.open('w') as output:
        log.chmod(0o600)
        subprocess.run(['bash',str(PAYLOAD/'host-test-old/install.sh'),DOMAIN],check=True,stdout=output,stderr=output)
    values=(ROOT/'api.env').read_text().replace('UPDATE_OWNER_ENABLED=false','UPDATE_OWNER_ENABLED=true')
    (ROOT/'api.env').write_text(values)
    run('docker','compose','--project-directory',str(ROOT),'-f',str(ROOT/'compose.yml'),'up','-d','--pull','never')
    wait_ready('host-test-old')
    finish_initialization()


def finish_initialization():
    if db('SELECT count(*) FROM users')[0][0]!=0:raise ValueError('refusing existing owner')
    wait_ready('host-test-old')
    # Signing credentials are private, ephemeral and remain outside runtime/bundles.
    seed=secrets.token_bytes(32)
    (FIXTURE/'signing-seed').write_text(base64.b64encode(seed).decode());(FIXTURE/'signing-seed').chmod(0o600)
    # release-tool uses a stdlib Ed25519 key; obtain its public key with OpenSSL PKCS8.
    pkcs8=bytes.fromhex('302e020100300506032b657004220420')+seed
    (FIXTURE/'seed.der').write_bytes(pkcs8);(FIXTURE/'seed.der').chmod(0o600)
    public=subprocess.check_output(['openssl','pkey','-inform','DER','-in',str(FIXTURE/'seed.der'),'-pubout','-outform','DER'],stderr=subprocess.DEVNULL)[-32:]
    private_json(Path('/etc/streamtool/release-trust.json'),{'public_key':base64.b64encode(public).decode(),'channel':'stable','artifact_origin':'https://release.acceptance.invalid'})
    owner=Owner();owner.enroll()
    private_json(FIXTURE/'baseline.json',{'fingerprint':fingerprint()})
    print('PASS: actual Linux installer, API/Agent/edge/public TLS health, owner MFA and preserved configuration fixture')


def offer(version,sequence,schema):
    archive=PAYLOAD/(version+'.tar.gz')
    with archive.open('rb') as incoming:stamp=hashlib.file_digest(incoming,'sha256').hexdigest()
    pointer=JOURNAL/'candidate.json'
    if pointer.exists():
        digest=json.loads(pointer.read_text())['digest']
        previous=json.loads((JOURNAL/'catalog'/digest/'manifest.json').read_text())
        if previous['sequence']==sequence and previous['version']==version and previous['target_schema']==schema and previous['artifacts'][0]['sha256']==stamp:
            # Exact immutable retry; root/API still recheck signature and expiry.
            return digest
    current_schema=db('SELECT count(*) FROM schema_migrations')[0][0]
    now=time.time()
    def timestamp(value):return time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime(value))
    manifest={'target_schema':schema,'sequence':sequence,'version':version,'channel':'stable','created':timestamp(now-30),'expires':timestamp(now+3600),'protocol':1,'minimum_sequence':0,'minimum_schema':current_schema,'maximum_schema':current_schema,'notes':'isolated host acceptance fixture','artifacts':[{'architecture':'arm64','url':'https://release.acceptance.invalid/bundle.tar.gz','size':archive.stat().st_size,'sha256':stamp}]}
    directory=FIXTURE/('release-'+str(sequence)+'-'+str(uuid.uuid4()));directory.mkdir(exist_ok=True)
    private_json(directory/'manifest.json',manifest)
    run(str(RUNTIME/'release-tool'),'sign','--key',str(FIXTURE/'signing-seed'),'--manifest',str(directory/'manifest.json'),'--signature',str(directory/'manifest.sig'))
    run('python3',str(RUNTIME/'updater_bridge.py'),'offer','--manifest',str(directory/'manifest.json'),'--signature',str(directory/'manifest.sig'),'--bundle',str(archive))
    return hashlib.sha256((directory/'manifest.json').read_bytes()).hexdigest()


def submit(owner,digest):
    job_id=str(uuid.uuid4())
    owner.request('POST','/v1/me/updates',{'request_id':job_id,'release_digest':digest,'code':owner.fresh_code()},expect=202)
    private_json(FIXTURE/'last-job.json',{'id':job_id,'old_cookie':owner.cookie})
    return job_id


def wait_job(job_id,phase,timeout=240):
    deadline=time.time()+timeout
    while time.time()<deadline:
        path=JOURNAL/job_id/'job.json'
        if path.exists():
            job=json.loads(path.read_text())
            if job['phase']==phase:return job
            if job['phase'] in ('SUCCEEDED','ROLLED_BACK','FAILED','RECOVERY_REQUIRED'):raise ValueError('unexpected terminal phase '+job['phase'])
        time.sleep(0.1)
    raise ValueError('host job did not reach '+phase)


def assert_preserved():
    if fingerprint()!=json.loads((FIXTURE/'baseline.json').read_text())['fingerprint']:raise ValueError('owner/source/output/key data changed')
    if (JOURNAL.parent/'admission/active.json').exists():raise ValueError('maintenance remained active')


def update_success():
    stamp=offer('host-test-new',7,8)
    owner=Owner();owner.login()
    status=owner.request('GET','/v1/me/updates')
    if status['release']['digest']!=stamp:raise ValueError('signed catalog digest mismatch')
    job_id=submit(owner,stamp);wait_job(job_id,'SUCCEEDED');wait_ready('host-test-new')
    assert_preserved()
    if db('SELECT value FROM host_acceptance_probe')!=[('successful migration',)]:raise ValueError('new migration not applied')
    if db('SELECT count(*) FROM schema_migrations')[0][0]!=8:raise ValueError('new schema mismatch')
    print('PASS: password-free owner TOTP → real Unix inbox → systemd update → actual version/schema/health, preserved keys/configuration')


def update_bad():
    stamp=offer('host-test-bad',8,9);owner=Owner();owner.login()
    job_id=submit(owner,stamp);job=wait_job(job_id,'ROLLED_BACK');wait_ready('host-test-new')
    assert_preserved()
    if not job.get('restored_credentials_revoked'):raise ValueError('restoration credential revocation missing')
    for table in ('user_sessions','owner_recovery','auth_enrollment'):
        if db('SELECT count(*) FROM '+table)[0][0]!=0:raise ValueError('restored authentication capability survived')
    owner.request('GET','/v1/me',expect=401)
    if db('SELECT count(*) FROM schema_migrations')[0][0]!=8:raise ValueError('rollback schema mismatch')
    print('PASS: actual failed API migration/startup automatically restored matching old OCI/Agent/UI/SQLite and revoked sessions/recovery')


def interrupt_and_reboot():
    stamp=offer('host-test-new',9,8);owner=Owner();owner.login()
    job_id=submit(owner,stamp);wait_job(job_id,'INSTALLING')
    private_json(FIXTURE/'reboot-checkpoint.json',{'boot_id':Path('/proc/sys/kernel/random/boot_id').read_text().strip()})
    run('systemctl','kill','--kill-whom=main','--signal=SIGKILL','streamtool-updater.service')
    # Restart occurs before the normal ten-second updater retry. The boot-enabled
    # independent coordinator must resume uncertain mutation as rollback.
    print('CHECKPOINT: actual coordinator killed in INSTALLING; rebooting disposable guest',flush=True)
    run('systemctl','reboot')


def after_reboot():
    checkpoint=json.loads((FIXTURE/'reboot-checkpoint.json').read_text())
    if checkpoint['boot_id']==Path('/proc/sys/kernel/random/boot_id').read_text().strip():raise ValueError('guest did not reboot')
    job_id=json.loads((FIXTURE/'last-job.json').read_text())['id']
    job=wait_job(job_id,'ROLLED_BACK');wait_ready('host-test-new');assert_preserved()
    if not job.get('restored_credentials_revoked'):raise ValueError('reboot restoration credential revocation missing')
    for table in ('user_sessions','owner_recovery','auth_enrollment'):
        if db('SELECT count(*) FROM '+table)[0][0]!=0:raise ValueError('reboot restored authentication capability survived')
    owner=Owner();owner.cookie=json.loads((FIXTURE/'last-job.json').read_text())['old_cookie'];owner.request('GET','/v1/me',expect=401)
    if db('SELECT count(*) FROM schema_migrations')[0][0]!=8:raise ValueError('reboot rollback schema mismatch')
    print('PASS: actual guest reboot resumed automatic offline matching-version rollback after killed INSTALLING coordinator')


def main():
    guard();parser=argparse.ArgumentParser();parser.add_argument('action',choices=('init','init-retry','success','bad','reboot','after-reboot','init-resume'));args=parser.parse_args()
    {'init':initialize,'init-resume':finish_initialization,'init-retry':lambda:initialize(retry=True),'success':update_success,'bad':update_bad,'reboot':interrupt_and_reboot,'after-reboot':after_reboot}[args.action]()


if __name__=='__main__':
    try:main()
    except Exception as error:
        print('FAIL: host acceptance '+str(error),file=sys.stderr);sys.exit(1)
