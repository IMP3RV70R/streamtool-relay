#!/usr/bin/env python3
"""Extract audited plugins and their ELF closure, retaining package scan metadata."""
import pathlib, re, shutil, subprocess
root = pathlib.Path('/minimal-media')
root.mkdir()
plugins = ['coreelements','srt','mpegtsdemux','videoparsersbad','audioparsers','inter','videotestsrc','audiotestsrc','imagefreeze','isomp4','png','x264','openh264','faad','voaacenc','flv','rtmp','debugutilsbad','videoconvertscale','videorate','audioconvert','audioresample']
source_prefix = pathlib.Path('/opt/streamtool-media')
plugin_dir = source_prefix/'lib/gstreamer-1.0'
paths = [plugin_dir / ('libgst'+name+'.so') for name in plugins]
paths += [source_prefix/'bin/gst-inspect-1.0', next(source_prefix.rglob('gst-plugin-scanner'))]
# Include the actual worker and the managed startup barrier, not a base OS.
worker = pathlib.Path('/out/stream-worker')
# Private file transport is implemented by the worker itself. Only the managed
# shell barrier still needs distribution utilities.
paths += [worker] + [pathlib.Path('/usr/bin') / name for name in ['dash', 'sleep']]
for name, target in [('lib', 'usr/lib'), ('lib64', 'usr/lib64'), ('bin', 'usr/bin')]:
    (root / name).symlink_to(target)
files=set(paths)
for path in paths:
    if not path.is_file(): raise SystemExit('Missing required plugin: '+str(path))
    probe=subprocess.run(['ldd',str(path)],text=True,capture_output=True)
    result=probe.stdout+probe.stderr
    if probe.returncode != 0:
        raise SystemExit('Cannot inventory runtime dependencies: '+str(path))
    if 'not found' in result: raise SystemExit('Missing runtime dependency: '+result)
    files.update(pathlib.Path(p) for p in re.findall(r'(?:=>\s+|^\s*)(/\S+)',result,re.M))
packages=set()
for path in sorted(files):
    target = pathlib.Path('/usr/local/bin/stream-worker') if path == worker else path
    if str(target).startswith(('/lib/', '/lib64/')):
        target = pathlib.Path('/usr' + str(target))
    destination=root/str(target).lstrip('/')
    destination.parent.mkdir(parents=True,exist_ok=True)
    shutil.copyfile(path,destination); shutil.copymode(path,destination)
    if path == worker:
        continue  # Our Go binary is inventoried by Trivy separately.
    if path.is_relative_to(source_prefix):
        continue  # Inventoried as upstream sources, not fake distribution packages.
    owner=subprocess.check_output(['dpkg-query','-S',str(target)],text=True).split(': ',1)[0]
    packages.add(owner)
    copyright_file=pathlib.Path('/usr/share/doc')/owner.split(':')[0]/'copyright'
    if copyright_file.is_file():
        copyright_target=root/'usr/share/streamtool/licenses'/owner.split(':')[0]/'copyright'
        copyright_target.parent.mkdir(parents=True,exist_ok=True)
        shutil.copyfile(copyright_file,copyright_target)
# Conservative metadata: scanners still see the originating plugin package even
# when only selected files were extracted. No CVEs are suppressed by extraction.
status='\n\n'.join(subprocess.check_output(['dpkg-query','-s',package],text=True).strip() for package in sorted(packages))+'\n\n'
(root/'var/lib/dpkg').mkdir(parents=True, exist_ok=True)
(root/'var/lib/dpkg/status').write_text(status)
(root/'usr/share/streamtool').mkdir(parents=True, exist_ok=True)
(root/'usr/share/streamtool/runtime-files.txt').write_text('\n'.join(map(str,sorted(files)))+'\n')

(root/'usr/bin/sh').symlink_to('dash')
(root/'usr/bin/gst-inspect-1.0').symlink_to('/opt/streamtool-media/bin/gst-inspect-1.0')
shutil.copytree(source_prefix/'share/streamtool', root/'usr/share/streamtool/upstream')
# Keep distribution identification, certificates and their conservative package
# metadata. There is no package manager or unselected utility in the final image.
for name in ['/etc/os-release', '/etc/debian_version', '/etc/ssl/certs/ca-certificates.crt']:
    destination = root / name.lstrip('/')
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(name, destination)
with (root/'var/lib/dpkg/status').open('a') as output:
    for package in ['ca-certificates', 'base-files']:
        output.write(subprocess.check_output(['dpkg-query', '-s', package], text=True).strip()+'\n\n')
for package in ['ca-certificates', 'base-files']:
    license_dir = root/'usr/share/streamtool/licenses'/package
    license_dir.mkdir(parents=True, exist_ok=True)
    shutil.copyfile('/usr/share/doc/'+package+'/copyright', license_dir/'copyright')
(root/'etc/nsswitch.conf').write_text('hosts: files dns\n')
(root/'tmp').mkdir(mode=0o1777)
(root/'tmp').chmod(0o1777)
(root/'run/secrets').mkdir(parents=True)
import os
os.chown(root/'run/secrets', 65532, 65532)
# Validate in the extracted filesystem: host libraries must not mask omissions.
for element in ['srtsrc','tsdemux','openh264dec','faad','voaacenc','x264enc','flvmux','rtmpsink','intervideosrc','interaudiosrc','pngdec','qtdemux','errorignore']:
    subprocess.run(['chroot', str(root), '/usr/bin/gst-inspect-1.0', element], check=True, stdout=subprocess.DEVNULL)
subprocess.run(['chroot', str(root), '/bin/sh', '-c', 'sleep 0.01'], check=True, stdout=subprocess.DEVNULL)

# Keep the security review's absent-component assumptions executable.
for element in ['rfbsrc', 'dtlsdec', 'dtlsenc', 'rtpsbcdepay', 'wavpackdec', 'asfdemux', 'gdkpixbufdec']:
    result = subprocess.run(['chroot', str(root), '/usr/bin/gst-inspect-1.0', element],
                            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if result.returncode == 0:
        raise SystemExit('Unexpected excluded element in worker: ' + element)
