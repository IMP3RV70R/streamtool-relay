#!/usr/bin/env python3
"""Fail-closed upstream scan bound to an immutable worker image.

Trivy's Debian scan is still required. Grype separately scans the native source
SBOM, after detecting a known vulnerable control with the same database.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

LOCK = Path(__file__).resolve().parents[2]/'backend/scripts/media-sources.json'
CANARY_CVE = 'CVE-2026-3085'
UTILITY_CANARY_CVE = 'CVE-2021-3995'
SCANNER = 'anchore/grype:v0.118.0'


def expected_sources(lock):
    options = ['--prefix=/opt/streamtool-media','--libdir=lib','--buildtype=release',
               '--wrap-mode=nodownload','-Dauto_features=disabled','-Dnls=disabled']
    expected = {}
    for p in lock['components']:
        url = f"https://gstreamer.freedesktop.org/src/{p['name']}/{p['name']}-{lock['version']}.tar.xz"
        expected[p['name']] = dict(p,version=lock['version'],url=url,
                                   build_options=options+['-D'+o for o in p['options']])
    p = lock['utility']
    expected[p['name']] = dict(p,build_options=['--prefix=/opt/streamtool-media',
                                               '--libdir=/opt/streamtool-media/lib']+p['options'])
    return expected


def component_cpe(name, version):
    product = 'kernel:util-linux' if name == 'util-linux' else 'gstreamer:gstreamer'
    return f'cpe:2.3:a:{product}:{version}:*:*:*:*:*:*:*'


def validate_inventory(directory, lock):
    provenance = json.loads((directory/'media-provenance.json').read_text())
    sbom = json.loads((directory/'media.cdx.json').read_text())
    expected = expected_sources(lock)
    if len(provenance) != len(expected) or {p['name'] for p in provenance} != set(expected):
        raise ValueError('Upstream provenance coverage incomplete')
    for p in provenance:
        source = expected[p['name']]
        if any(p.get(k) != source[k] for k in ['name','sha256','options','version','url','build_options']):
            raise ValueError('Upstream provenance does not match source lock')
        if not any(p.is_file() for p in (directory/'licenses'/p['name']).rglob('*')):
            raise ValueError('Upstream source license missing')
    components = sbom.get('components', [])
    if sbom.get('bomFormat') != 'CycloneDX' or sbom.get('specVersion') != '1.6' or len(components) != len(expected) or {c['name'] for c in components} != set(expected):
        raise ValueError('Upstream SBOM coverage incomplete')
    for c in components:
        version = expected[c['name']]['version']
        if (c.get('version') != version or c.get('type') != 'library'
                or c.get('purl') != f"pkg:generic/{c['name']}@{version}"
                or c.get('cpe') != component_cpe(c['name'],version)
                or c.get('hashes') != [{'alg':'SHA-256','content':expected[c['name']]['sha256']}]):
            raise ValueError('Upstream SBOM identity invalid')
    return sbom


def validate_scan(control, actual):
    matches = control['matches']
    for cve, name, version in [(CANARY_CVE,'gstreamer','1.26.2'),
                               (UTILITY_CANARY_CVE,'util-linux','2.37.2')]:
        if not any(m.get('vulnerability', {}).get('id') == cve
                   and m.get('artifact', {}).get('name') == name
                   and m.get('artifact', {}).get('version') == version for m in matches):
            raise ValueError('Upstream CPE coverage control not detected: '+name)
    for report in [control, actual]:
        if not isinstance(report.get('matches'), list) or report.get('descriptor', {}).get('name') != 'grype':
            raise ValueError('Upstream scanner report incomplete')
        if report.get('descriptor', {}).get('version') != '0.118.0':
            raise ValueError('Unexpected upstream scanner version')
    control_db = control['descriptor'].get('db', {}).get('status', {})
    actual_db = actual['descriptor'].get('db', {}).get('status', {})
    if (control_db.get('valid') is not True or actual_db.get('valid') is not True
            or not control_db.get('from') or not control_db.get('built')
            or any(actual_db.get(k) != control_db[k] for k in ['from','built'])):
        raise ValueError('Upstream control and actual scan database differ')


def main(image, reports):
    if not re.fullmatch(r'sha256:[a-f0-9]{64}', image):
        raise ValueError('Immutable worker ID required')
    reports = Path(reports)
    lock = json.loads(LOCK.read_text())
    container = None
    # Debian may mount the host's /tmp as a small tmpfs. Keep database download
    # and hydration on the artifact filesystem as well as outside container tmpfs.
    with tempfile.TemporaryDirectory(prefix='.upstream-scan-', dir=reports.resolve()) as tmp:
        tmp = Path(tmp);tmp.chmod(0o755)
        try:
            container = subprocess.check_output(['docker','create','--network','none',image],text=True).strip()
            subprocess.run(['docker','cp',container+':/usr/share/streamtool/upstream',str(tmp/'upstream')],check=True)
            sbom = validate_inventory(tmp/'upstream', lock)
            for name in ['media-provenance.json','media.cdx.json']:
                (reports/name).write_bytes((tmp/'upstream'/name).read_bytes())
            control = json.loads(json.dumps(sbom))
            versions = {'gstreamer':'1.26.2','util-linux':'2.37.2'}
            control['components'] = [c for c in control['components'] if c['name'] in versions]
            for c in control['components']:
                c['version'] = versions[c['name']]
                c['purl'] = f"pkg:generic/{c['name']}@{c['version']}"
                c['cpe'] = component_cpe(c['name'],c['version'])
                c.pop('hashes',None)
            (tmp/'control.json').write_text(json.dumps(control))
            cache = tmp/'cache';cache.mkdir(mode=0o777);cache.chmod(0o777)
            user = '65532:65532' if os.getuid() == 0 else f'{os.getuid()}:{os.getgid()}'
            args = ['docker','run','--rm','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges',
                    '--user',user,'--tmpfs','/tmp:rw,noexec,nosuid,nodev,mode=1777,size=1g',
                    '--env','GRYPE_DB_CACHE_DIR=/cache','--env','GRYPE_CHECK_FOR_APP_UPDATE=false',
                    '--env','SQLITE_TMPDIR=/cache','--env','TMPDIR=/cache',
                    '--mount',f'type=bind,src={cache},dst=/cache',
                    '--mount',f'type=bind,src={tmp},dst=/scan,readonly']
            scanned = []
            for name, path, update in [('source-coverage-control','control.json',True),('worker-upstream','upstream/media.cdx.json',False)]:
                env = ['--env','GRYPE_DB_REQUIRE_UPDATE_CHECK=true'] if update else ['--env','GRYPE_DB_AUTO_UPDATE=false']
                result = subprocess.run(args+env+[SCANNER,'sbom:/scan/'+path,'--fail-on','high','-o','json'],
                                        text=True,capture_output=True,timeout=600)
                (reports/(name+'.json')).write_text(result.stdout)
                (reports/(name+'.log')).write_text(result.stderr)
                if result.returncode not in [0,2]:
                    raise ValueError('Upstream scanner unavailable (exit '+str(result.returncode)+'); see '+name+'.log')
                scanned.append((result.returncode,json.loads(result.stdout)))
            validate_scan(scanned[0][1],scanned[1][1])
            (reports/'upstream-worker-image').write_text(image+'\n')
            if scanned[1][0] != 0 or any(m['vulnerability']['severity'].upper() in ['HIGH','CRITICAL'] for m in scanned[1][1]['matches']):
                raise ValueError('Upstream HIGH/CRITICAL findings remain')
            print('worker-upstream: PASS (known-vulnerable control detected)')
        finally:
            if container:
                subprocess.run(['docker','rm',container],check=True,stdout=subprocess.DEVNULL)


if __name__ == '__main__':
    try:
        if len(sys.argv) != 3:
            raise ValueError('Usage: media_scan.py IMMUTABLE_WORKER_ID REPORT_DIRECTORY')
        main(*sys.argv[1:])
    except (ValueError, OSError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print('worker-upstream: FAIL:',str(error),file=sys.stderr)
        sys.exit(1)
