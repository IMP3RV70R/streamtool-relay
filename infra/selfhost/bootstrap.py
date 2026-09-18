#!/usr/bin/env python3
"""Create private installation configuration without replacing existing secrets."""
import argparse
import base64
import json
import ipaddress
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import tempfile
import uuid


def durable_replace(temporary, target):
    with temporary.open('rb') as file: os.fsync(file.fileno())
    os.replace(temporary, target)
    fd = os.open(target.parent, os.O_RDONLY | os.O_DIRECTORY)
    try: os.fsync(fd)
    finally: os.close(fd)


def durable_write(path, data):
    fd, name = tempfile.mkstemp(prefix='.bootstrap-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as file:
            os.fchmod(file.fileno(),0o600); file.write(data); file.flush(); os.fsync(file.fileno())
        durable_replace(Path(name),path)
    finally:
        if os.path.exists(name): os.unlink(name)


def public_address(value):
    """Return the URL authority and ACME profile; never resolve user input here."""
    try:
        address = ipaddress.ip_address(value)
    except ValueError:
        if not re.fullmatch(r'(?=.{1,253}$)(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}', value):
            raise ValueError('a public IP address or DNS hostname is required') from None
        return value.lower(), 'tlsserver'
    if not address.is_global or address.is_multicast or address.is_unspecified or address.is_reserved or getattr(address, 'ipv4_mapped', None) or '%' in value:
        raise ValueError('a public unicast IP address is required')
    host = str(address)
    return ('[' + host + ']' if address.version == 6 else host), 'shortlived'


def initialize(directory, domain, api_image, worker_image, proxy_image, edge_image, *, resume=False, runtime_directory=None):
    domain, profile = public_address(domain)
    for image in (api_image, worker_image, proxy_image, edge_image):
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_./:@-]{0,255}', image):
            raise ValueError('invalid image reference')
    if resume:
        marker = directory / 'INSTALLATION_JOB'
        job = str(uuid.UUID(marker.read_text().strip()))
        if directory.is_symlink() or marker.is_symlink() or directory.name != '.streamtool-bootstrap-' + job:
            raise ValueError('resume requires an owned private bootstrap stage')
    if (directory / 'api.env').exists() and not resume:
        raise ValueError('installation already initialized; use the update command')
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    directory.chmod(0o700)
    runtime_directory = runtime_directory or directory
    os.umask(0o077)
    for name in ('state', 'secrets', 'certs', 'caddy-data', 'caddy-config'):
        (directory / name).mkdir(mode=0o700, exist_ok=True)
    for name in ('setup_token','admin_token','edge_control_token','edge_read_key','envelope_key'):
        path = directory / 'secrets' / name
        if not path.exists():
            durable_write(path, (base64.b64encode(secrets.token_bytes(32)).decode()+'\n').encode())
        if path.is_symlink() or len(base64.b64decode(path.read_text().strip(),validate=True)) != 32:
            raise ValueError('invalid existing installation secret')
        path.chmod(0o600)
    certs=directory/'certs'
    def openssl(*args):
        subprocess.run(['openssl',*args],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    def create_key(path, bits):
        if path.exists():
            openssl('pkey','-in',str(path),'-noout'); return
        temporary = path.with_suffix('.key.new')
        openssl('genpkey','-algorithm','RSA','-pkeyopt',f'rsa_keygen_bits:{bits}','-out',str(temporary))
        openssl('pkey','-in',str(temporary),'-noout')
        durable_replace(temporary, path)
    if (certs/'ca.crt').exists() and not (certs/'ca.key').exists():
        raise ValueError('existing CA is missing its private key')
    create_key(certs/'ca.key',3072)
    if not (certs/'ca.crt').exists():
        temporary = certs/'ca.crt.new'
        openssl('req','-x509','-new','-key',str(certs/'ca.key'),'-out',str(temporary),'-days','3650','-subj','/CN=streamtool-private-ca','-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign')
        durable_replace(temporary,certs/'ca.crt')
    certificates={
        'api':('streamtool-api','DNS:api,IP:172.30.82.2','serverAuth'),
        'edge':('streamtool-edge','DNS:edge','serverAuth'),
        'agent':('streamtool-agent','IP:172.30.80.1','serverAuth'),
        'controller':('streamtool-controller',None,'clientAuth'),
    }
    for name,(cn,san,usage) in certificates.items():
        key=certs/f'{name}.key'; certificate=certs/f'{name}.crt'
        if key.exists() and certificate.exists():
            openssl('verify','-CAfile',str(certs/'ca.crt'),str(certificate))
            continue
        if certificate.exists():
            raise ValueError('existing certificate is missing its private key')
        create_key(key,2048)
        openssl('req','-new','-key',str(key),'-out',str(certs/f'{name}.csr'),'-subj','/CN='+cn)
        ext=certs/f'{name}.ext'
        ext.write_text('extendedKeyUsage='+usage+'\n'+('subjectAltName='+san+'\n' if san else ''))
        openssl('x509','-req','-in',str(certs/f'{name}.csr'),'-CA',str(certs/'ca.crt'),'-CAkey',str(certs/'ca.key'),'-CAcreateserial','-out',str(certs/f'{name}.crt'),'-days','365','-extfile',str(ext))
        ext.unlink(); (certs/f'{name}.csr').unlink()
    (directory/'keyring.json').write_text(json.dumps({'current':'installation-v1','keys':{'installation-v1':{'file':'/run/secrets/envelope_key'}}})+'\n')
    node='00000000-0000-4000-8000-000000000001'
    (directory/'media-node.json').write_text(json.dumps([{'id':node,'agent_url':'https://172.30.80.1:8443','slots':1}])+'\n')
    settings={
        'MAINTENANCE_DIRECTORY':'/maintenance',
        'UPDATE_SOCKET':'/updater/control.sock','UPDATE_OWNER_ENABLED':'false',
        'STREAMTOOL_ENV':'production','SQLITE_PATH':'/data/control.sqlite',
        'SQLITE_MIGRATIONS_DIR':'/sqlite-migrations','WEB_DIR':'/web',
        'PUBLIC_RTMP_URL':f'rtmp://{domain}:1935','PUBLIC_SRT_URL':f'srt://{domain}:8890',
        'ENVELOPE_PROVIDER':'file','ENVELOPE_MANIFEST_FILE':'/deployment/keyring.json',
        'SETUP_TOKEN_FILE':'/run/secrets/setup_token','API_ADMIN_TOKEN_FILE':'/run/secrets/admin_token',
        'EDGE_CONTROL_TOKEN_FILE':'/run/secrets/edge_control_token','EDGE_READ_KEY_FILE':'/run/secrets/edge_read_key',
        'API_TLS_CERT_FILE':'/certs/api.crt','API_TLS_KEY_FILE':'/certs/api.key',
        'TRUSTED_PROXY_CIDRS':'172.30.80.3/32','EDGE_AUTH_ADDR':':8082',
        'EDGE_AUTH_PEER_CIDRS':'172.30.82.3/32','EDGE_AUTH_TLS_CERT_FILE':'/certs/api.crt','EDGE_AUTH_TLS_KEY_FILE':'/certs/api.key',
        'MEDIA_NODES_FILE':'/deployment/media-node.json','AGENT_CA_FILE':'/certs/ca.crt',
        'CONTROLLER_CERT_FILE':'/certs/controller.crt','CONTROLLER_KEY_FILE':'/certs/controller.key',
        'NODE_REGION':'local','WORKER_IMAGE':worker_image,'EDGE_ID':'selfhost-edge',
        'EDGE_API_URL':'https://edge:9997','EDGE_API_CA_FILE':'/certs/ca.crt','EDGE_SRT_URL':'srt://172.30.81.2:8890',
    }
    (directory/'api.env').write_text(''.join(f'{k}={v}\n' for k,v in settings.items()))
    (directory/'.env').write_text(f'DOMAIN={domain}\nACME_PROFILE={profile}\nAPI_IMAGE={api_image}\nWORKER_IMAGE={worker_image}\nPROXY_IMAGE={proxy_image}\nEDGE_IMAGE={edge_image}\n')
    agent={
        'MAINTENANCE_DIRECTORY':'/var/lib/streamtool-updater/admission',
        'STREAMTOOL_ENV':'production','NODE_ID':node,'NODE_SLOTS':'1','NODE_CPU_MILLIS':'2000','NODE_MEMORY_BYTES':str(1<<30),
        'NODE_INGRESS_BPS':'20000000','NODE_EGRESS_BPS':'100000000','AGENT_ADDR':'172.30.80.1:8443',
        'WORKER_NETWORK':'streamtool-media','WORKER_IMAGE':worker_image,'AGENT_STATE_FILE':'/var/lib/streamtool/fence.json',
        'AGENT_CA_FILE':str(runtime_directory/'certs/ca.crt'),'AGENT_CERT_FILE':str(runtime_directory/'certs/agent.crt'),'AGENT_KEY_FILE':str(runtime_directory/'certs/agent.key'),
    }
    (directory/'node.env').write_text(''.join(f'{k}={v}\n' for k,v in agent.items()))
    (directory/'worker-network.env').write_text('WORKER_NETWORK=streamtool-media\nSOURCE_EDGE_IP=172.30.81.2\nSOURCE_EDGE_PORT=8890\n')
    for name in ('compose.yml','Caddyfile','mediamtx.yml'):
        shutil.copyfile(Path(__file__).parent/name,directory/name)


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('directory',type=Path);parser.add_argument('domain')
    parser.add_argument('--api-image',required=True);parser.add_argument('--worker-image',required=True)
    parser.add_argument('--proxy-image',required=True);parser.add_argument('--edge-image',required=True)
    args=parser.parse_args()
    initialize(args.directory.resolve(),args.domain,args.api_image,args.worker_image,args.proxy_image,args.edge_image)
    print('Installation configuration created. Setup token is in secrets/setup_token.')
