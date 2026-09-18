#!/usr/bin/env python3
"""Build pinned upstream modules without Meson fallback downloads.

Distribution libraries retain their package inventory. These separately built
modules carry their actual source digests, configuration, licenses and SBOM;
they must not be represented as distribution-maintained GStreamer packages.
"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import urllib.request
import shutil

PREFIX = Path('/opt/streamtool-media')
lock = json.loads(Path('/media-sources.json').read_text())
version = lock['version']
env = dict(os.environ, PKG_CONFIG_PATH=str(PREFIX/'lib/pkgconfig'),
           LD_LIBRARY_PATH=str(PREFIX/'lib'))
provenance = []


def fetch_source(tmp, name, version, url, digest):
    archive = tmp/f'{name}-{version}.tar.xz'
    with urllib.request.urlopen(url, timeout=90) as response, archive.open('wb') as output:
        while data := response.read(1024*1024):
            output.write(data)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != digest:
        raise SystemExit(f'Source digest mismatch: {name}')
    with tarfile.open(archive) as source:
        source.extractall(tmp, filter='data')
    return tmp/f'{name}-{version}'


def copy_licenses(source_dir, name):
    license_dir = PREFIX/'share/streamtool/licenses'/name
    license_files = [p for p in source_dir.rglob('*')
                     if p.is_file() and p.name.startswith(('COPYING', 'LICENSE'))]
    if not license_files:
        raise SystemExit(f'Source licenses missing: {name}')
    for p in license_files:
        dest = license_dir/p.relative_to(source_dir)
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(p, dest)


with tempfile.TemporaryDirectory(prefix='streamtool-media-source-') as tmp:
    tmp = Path(tmp)
    utility = lock['utility']
    source_dir = fetch_source(tmp, utility['name'], utility['version'],
                              utility['url'], utility['sha256'])
    options = ['--prefix='+str(PREFIX),'--libdir='+str(PREFIX/'lib')]+utility['options']
    subprocess.run([str(source_dir/'configure'),*options],cwd=source_dir,env=env,check=True)
    subprocess.run(['make','-j2'],cwd=source_dir,env=env,check=True)
    subprocess.run(['make','install'],cwd=source_dir,env=env,check=True)
    copy_licenses(source_dir, utility['name'])
    provenance.append(dict(utility,build_options=options))
    for component in lock['components']:
        name = component['name']
        filename = f'{name}-{version}.tar.xz'
        url = f'https://gstreamer.freedesktop.org/src/{name}/{filename}'
        source_dir = fetch_source(tmp,name,version,url,component['sha256'])
        build_dir = tmp/f'{name}-build'
        options = ['--prefix='+str(PREFIX), '--libdir=lib', '--buildtype=release',
                   '--wrap-mode=nodownload', '-Dauto_features=disabled', '-Dnls=disabled']
        options += ['-D'+option for option in component['options']]
        subprocess.run(['meson', 'setup', str(build_dir), str(source_dir), *options],
                       env=env, check=True)
        subprocess.run(['ninja', '-C', str(build_dir), '-j', '2'], env=env, check=True)
        subprocess.run(['meson', 'install', '-C', str(build_dir), '--no-rebuild'],
                       env=env, check=True)
        copy_licenses(source_dir,name)
        provenance.append(dict(component, version=version, url=url,
                               build_options=options))
inventory = PREFIX/'share/streamtool'
inventory.mkdir(parents=True, exist_ok=True)
(inventory/'media-provenance.json').write_text(json.dumps(provenance, indent=2)+'\n')
sbom = {'bomFormat':'CycloneDX', 'specVersion':'1.6', 'version':1,
        'components':[{'type':'library', 'name':p['name'], 'version':p['version'],
                       'bom-ref':p['name'], 'purl':f"pkg:generic/{p['name']}@{p['version']}",
                       'cpe':f"cpe:2.3:a:{'kernel:util-linux' if p['name']=='util-linux' else 'gstreamer:gstreamer'}:{p['version']}:*:*:*:*:*:*:*",
                       'hashes':[{'alg':'SHA-256', 'content':p['sha256']}],
                       'externalReferences':[{'type':'distribution', 'url':p['url']}]}
                      for p in provenance]}
(inventory/'media.cdx.json').write_text(json.dumps(sbom, indent=2)+'\n')
