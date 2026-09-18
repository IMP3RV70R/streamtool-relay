"""Prepare private host-test bundles from current binaries and existing media OCI.
Only uniquely named test API images are built. No application is deployed here.
"""
import argparse
from pathlib import Path
import shutil
import subprocess
import tarfile

REPOSITORY=Path(__file__).resolve().parents[3]


def command(*arguments):
    return subprocess.check_output(arguments,stderr=subprocess.DEVNULL,text=True).strip()


def main():
    parser=argparse.ArgumentParser();parser.add_argument('directory',type=Path)
    args=parser.parse_args();root=args.directory.resolve()
    build=root/'build'
    payload=root/'payload'/'streamtool-host-acceptance'
    if payload.exists(): raise ValueError('payload already exists')
    payload.mkdir(parents=True,mode=0o700)
    images={name:command('docker','image','inspect','--format','{{.Id}}',reference)
            for name,reference in {'worker':'streamtool-relay-worker:0.1.0-rc8',
              'proxy':'streamtool-relay-proxy:0.1.0-rc8','edge':'bluenviron/mediamtx:1.21.0'}.items()}
    versions={'old':'host-test-old','new':'host-test-new','bad':'host-test-bad'}
    for flavor,version in versions.items():
        context=root/('context-'+flavor);context.mkdir()
        shutil.copyfile(build/('api-'+flavor),context/'api');(context/'api').chmod(0o755)
        shutil.copytree(REPOSITORY/'backend/sqlite-migrations',context/'migrations')
        if flavor!='old':
            (context/'migrations/000008_host_acceptance.up.sql').write_text("CREATE TABLE host_acceptance_probe(value TEXT); INSERT INTO host_acceptance_probe VALUES('successful migration');\n")
        if flavor=='bad':
            (context/'migrations/000009_host_acceptance_failure.up.sql').write_text('THIS IS INTENTIONALLY INVALID SQL;\n')
        (context/'Dockerfile').write_text('FROM streamtool-relay-api:0.1.0-rc8\nCOPY --chmod=755 api /usr/local/bin/api\nCOPY migrations /sqlite-migrations\n')
        tag='streamtool-relay-host-test-api-'+flavor+':local'
        subprocess.run(['docker','build','--quiet','-t',tag,str(context)],check=True,stdout=subprocess.DEVNULL)
        api=command('docker','image','inspect','--format','{{.Id}}',tag)
        bundle=payload/version;bundle.mkdir()
        for path in (REPOSITORY/'infra/selfhost').iterdir():
            if path.is_file(): shutil.copyfile(path,bundle/path.name)
        for name in ('streamtool-agent.service','streamtool-agent.sudoers','streamtool-worker-policy'):
            shutil.copyfile(REPOSITORY/'infra/vm'/name,bundle/name)
        shutil.copytree(REPOSITORY/'apps/web',bundle/'web')
        shutil.copyfile(build/('agent-'+flavor),bundle/'node-agent')
        shutil.copyfile(build/'release-tool',bundle/'release-tool')
        for name in ('node-agent','release-tool'): (bundle/name).chmod(0o755)
        (bundle/'VERSION').write_text(version+'\n');(bundle/'ARCHITECTURE').write_text('arm64\n')
        for name,image in {'api':api,**images}.items(): (bundle/(name+'-image')).write_text(image+'\n')
        # Controlled CA on the disposable guest; ordinary hostname verification
        # remains enabled. This is not public certificate issuance acceptance.
        caddy=(bundle/'Caddyfile').read_text()
        start=caddy.index('    tls {'); end=caddy.index('    header ',start)
        caddy=caddy[:start]+'    tls /host-test/tls/relay.crt /host-test/tls/relay.key\n'+caddy[end:]
        (bundle/'Caddyfile').write_text(caddy)
        compose=(bundle/'compose.yml').read_text().replace('      - ./Caddyfile:/etc/caddy/Caddyfile:ro','      - ./Caddyfile:/etc/caddy/Caddyfile:ro\n      - /var/lib/streamtool-host-acceptance/tls:/host-test/tls:ro',1)
        (bundle/'compose.yml').write_text(compose)
        subprocess.run(['docker','save','-o',str(bundle/'images.tar'),api,*images.values()],check=True)
        subprocess.run(['python3',str(REPOSITORY/'infra/selfhost/archive.py'),str(bundle),str(payload/(version+'.tar.gz'))],check=True,stdout=subprocess.DEVNULL)
    for name in ('guest.py','matrix.py','storage.py','power.py','system_disk.py'):
        shutil.copyfile(Path(__file__).with_name(name),payload/name)
    with tarfile.open(root/'payload.tar.gz','w:gz') as archive:
        archive.add(payload,arcname=payload.name)
    print('PASS: current API/Agent test bundles prepared; media unchanged, no deployment')


if __name__=='__main__': main()
