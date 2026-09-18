"""Upstream provenance and coverage must fail closed, independently of findings."""
import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('media_scan',ROOT/'infra/release/media_scan.py')
scan = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scan)


class MediaScanTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='streamtool-media-inventory-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.lock = json.loads(scan.LOCK.read_text())
        self.records = list(scan.expected_sources(self.lock).values())
        self.sbom = {'bomFormat':'CycloneDX','specVersion':'1.6','components':[
            {'type':'library','name':p['name'],'version':p['version'],
             'purl':f"pkg:generic/{p['name']}@{p['version']}",
             'cpe':scan.component_cpe(p['name'],p['version']),
             'hashes':[{'alg':'SHA-256','content':p['sha256']}]} for p in self.records]}
        for p in self.records:
            directory = self.root/'licenses'/p['name']
            directory.mkdir(parents=True)
            (directory/'COPYING').write_text('local test license fixture')
        self.save()

    def save(self):
        (self.root/'media-provenance.json').write_text(json.dumps(self.records))
        (self.root/'media.cdx.json').write_text(json.dumps(self.sbom))

    def refused(self):
        self.save()
        with self.assertRaises(ValueError):
            scan.validate_inventory(self.root,self.lock)

    def test_complete_locked_inventory(self):
        self.assertEqual(len(scan.validate_inventory(self.root,self.lock)['components']),6)

    def test_changed_source_digest(self):
        self.records[0]['sha256']='0'*64
        self.refused()

    def test_unlocked_build_configuration(self):
        self.records[0]['build_options'].append('-Dauto_features=enabled')
        self.refused()

    def test_missing_utility_component(self):
        self.sbom['components']=[c for c in self.sbom['components'] if c['name']!='util-linux']
        self.refused()

    def test_unsupported_cpe_identity(self):
        self.sbom['components'][0]['cpe']='cpe:2.3:a:unknown:unknown:1.0:*:*:*:*:*:*:*'
        self.refused()

    def test_missing_source_license(self):
        (self.root/'licenses'/self.records[0]['name']/'COPYING').unlink()
        self.refused()

    def reports(self):
        descriptor = {'name':'grype','version':'0.118.0','db':{'status':{
            'valid':True,'from':'fixture-database','built':'fixture-time'}}}
        matches=[{'vulnerability':{'id':cve},'artifact':{'name':name,'version':version}}
                 for cve,name,version in [(scan.CANARY_CVE,'gstreamer','1.26.2'),
                                           (scan.UTILITY_CANARY_CVE,'util-linux','2.37.2')]]
        return ({'matches':matches,'descriptor':descriptor},
                {'matches':[],'descriptor':copy.deepcopy(descriptor)})

    def test_both_controls_and_same_valid_database(self):
        scan.validate_scan(*self.reports())

    def test_missing_utility_coverage_control(self):
        control,actual=self.reports();control['matches'].pop()
        with self.assertRaises(ValueError):scan.validate_scan(control,actual)

    def test_changed_or_invalid_database(self):
        for key,value in [('from','another-database'),('valid',False)]:
            control,actual=self.reports();actual['descriptor']['db']['status'][key]=value
            with self.assertRaises(ValueError):scan.validate_scan(control,actual)


if __name__ == '__main__':
    unittest.main()
