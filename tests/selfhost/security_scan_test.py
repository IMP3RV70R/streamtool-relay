"""Gate regressions with a fake CLI; real image evidence is recorded separately."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ScanCoverageTest(unittest.TestCase):
    def scan(self, report, scanner_exit=0):
        with tempfile.TemporaryDirectory(prefix="streamtool-scan-test-") as tmp:
            tmp = Path(tmp)
            bundle = tmp / "candidate"
            bundle.mkdir()
            image = "sha256:" + "1" * 64
            for role in ["api", "worker", "proxy", "edge"]:
                (bundle / (role + "-image")).write_text(image)
            cli = tmp / "docker"
            cli.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
if args[0] == 'image':
    print(args[-1])
elif args[0] == 'create':
    print('fake-worker-container')
elif args[0] == 'rm':
    pass
elif args[0] == 'cp':
    root=pathlib.Path(args[-1]);root.mkdir()
    lock=json.loads(pathlib.Path(os.environ['SOURCE_LOCK']).read_text())
    records=[];components=[]
    sources=[dict(source,version=lock['version']) for source in lock['components']]+[lock['utility']]
    for source in sources:
        name=source['name'];version=source['version']
        if name=='util-linux':
            options=['--prefix=/opt/streamtool-media','--libdir=/opt/streamtool-media/lib']+source['options']
            url=source['url'];product='kernel:util-linux'
        else:
            options=['--prefix=/opt/streamtool-media','--libdir=lib','--buildtype=release','--wrap-mode=nodownload','-Dauto_features=disabled','-Dnls=disabled']+['-D'+o for o in source['options']]
            url=f'https://gstreamer.freedesktop.org/src/{name}/{name}-{version}.tar.xz';product='gstreamer:gstreamer'
        records.append(dict(source,version=version,url=url,build_options=options))
        components.append(dict(type='library',name=name,version=version,purl=f'pkg:generic/{name}@{version}',cpe=f'cpe:2.3:a:{product}:{version}:*:*:*:*:*:*:*',hashes=[{'alg':'SHA-256','content':source['sha256']}]))
        license_dir=root/'licenses'/name;license_dir.mkdir(parents=True);(license_dir/'COPYING').write_text('test license fixture')
    (root/'media-provenance.json').write_text(json.dumps(records))
    (root/'media.cdx.json').write_text(json.dumps(dict(bomFormat='CycloneDX',specVersion='1.6',components=components)))
elif args[0] == 'save':
    pathlib.Path(args[args.index('-o')+1]).touch()
elif args[0] == 'run':
    if 'anchore/grype:v0.118.0' in args:
        control='sbom:/scan/control.json' in args
        matches=[{'vulnerability':{'id':cve,'severity':'High'},'artifact':{'name':name,'version':version}} for cve,name,version in [('CVE-2026-3085','gstreamer','1.26.2'),('CVE-2021-3995','util-linux','2.37.2')]] if control else []
        descriptor={'name':'grype','version':'0.118.0','db':{'status':{'valid':True,'built':'fixture-time','from':'fixture-database'}}}
        print(json.dumps({'matches':matches,'descriptor':descriptor}))
        sys.exit(2 if control else int(os.environ['FAKE_SCAN_EXIT']))
    report = json.loads(os.environ['FAKE_REPORT'])
    if report is not None:
        cache = next(a.split(',dst=')[0].split('src=')[1] for a in args if a.endswith('dst=/cache'))
        pathlib.Path(cache, 'report.json').write_text(json.dumps(report))
    sys.exit(int(os.environ['FAKE_SCAN_EXIT']))
else:
    sys.exit(2)
''')
            cli.chmod(0o755)
            env = dict(os.environ, PATH=str(tmp) + os.pathsep + os.environ['PATH'],
                       FAKE_REPORT=json.dumps(report), FAKE_SCAN_EXIT=str(scanner_exit),
                       SOURCE_LOCK=str(ROOT/'backend/scripts/media-sources.json'))
            result = subprocess.run(['bash', str(ROOT/'infra/release/scan.sh'), str(bundle)],
                                    env=env, capture_output=True, text=True, timeout=30)
            return result.returncode

    def test_supported_clean_inventory(self):
        self.assertEqual(self.scan({'Metadata': {'OS': {'Family': 'debian'}},
                                    'Results': [{'Class': 'os-pkgs', 'Type': 'debian'}]}), 0)

    def test_unsupported_os_cannot_pass(self):
        self.assertNotEqual(self.scan({'Metadata': {'OS': {'Family': 'none'}}, 'Results': []}), 0)

    def test_missing_package_coverage_cannot_pass(self):
        self.assertNotEqual(self.scan({'Metadata': {'OS': {'Family': 'debian'}}, 'Results': []}), 0)

    def test_missing_report_cannot_pass(self):
        self.assertNotEqual(self.scan(None), 0)

    def test_scanner_failure_cannot_pass(self):
        self.assertNotEqual(self.scan({'Metadata': {'OS': {'Family': 'debian'}},
                                     'Results': [{'Class': 'os-pkgs', 'Type': 'debian'}]}, 1), 0)


if __name__ == '__main__':
    unittest.main()
