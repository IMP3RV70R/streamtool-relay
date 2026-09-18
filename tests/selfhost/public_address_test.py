"""Public address configuration; no public ACME issuance is implied."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / 'infra/selfhost'))
import bootstrap


class PublicAddressTest(unittest.TestCase):
    def test_public_addresses_and_url_authorities(self):
        self.assertEqual(bootstrap.public_address('8.8.8.8'), ('8.8.8.8', 'shortlived'))
        self.assertEqual(bootstrap.public_address('2606:4700:4700::1111'), ('[2606:4700:4700::1111]', 'shortlived'))
        self.assertEqual(bootstrap.public_address('Stream.Example.COM'), ('stream.example.com', 'tlsserver'))

    def test_nonpublic_addresses_and_configuration_injection_are_refused(self):
        for value in ['127.0.0.1', '10.0.0.1', '100.64.0.1', '169.254.169.254', '192.0.2.1',
                      '224.0.0.1', '0.0.0.0', '::', '::1', 'fe80::1', 'fc00::1', 'ff02::1',
                      '::ffff:8.8.8.8', 'fe80::1%eth0', '[2606:4700::1111]', '8.8.8.8:22',
                      'https://example.com', 'example.com\nDOMAIN=attacker.com', 'example.com/path']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                bootstrap.public_address(value)

    def test_generated_ipv6_configuration_is_coherent(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / 'install'
            bootstrap.initialize(directory, '2606:4700:4700::1111', *['sha256:' + 'a' * 64] * 4)
            env = (directory / '.env').read_text()
            self.assertIn('DOMAIN=[2606:4700:4700::1111]\nACME_PROFILE=shortlived\n', env)
            api = (directory / 'api.env').read_text()
            self.assertIn('PUBLIC_RTMP_URL=rtmp://[2606:4700:4700::1111]:1935\n', api)
            self.assertIn('PUBLIC_SRT_URL=srt://[2606:4700:4700::1111]:8890\n', api)
            self.assertNotIn('tls internal', (directory / 'Caddyfile').read_text())

    @unittest.skipUnless(os.environ.get('CADDY_TEST_BINARY'), 'actual pinned Caddy adapter is opt-in')
    def test_actual_caddy_policies_use_public_acme_for_ip_and_dns(self):
        for host, profile in [('8.8.8.8', 'shortlived'), ('[2606:4700:4700::1111]', 'shortlived'), ('stream.example.com', 'tlsserver')]:
            with self.subTest(host=host):
                result = subprocess.check_output([os.environ['CADDY_TEST_BINARY'], 'adapt', '--config',
                    str(ROOT / 'infra/selfhost/Caddyfile'), '--adapter', 'caddyfile'],
                    env=dict(os.environ, DOMAIN=host, ACME_PROFILE=profile), stderr=subprocess.DEVNULL)
                config = json.loads(result)
                policies = config['apps']['tls']['automation']['policies']
                issuer = policies[0]['issuers'][0]
                self.assertEqual(issuer['module'], 'acme')
                self.assertEqual(issuer['ca'], 'https://acme-v02.api.letsencrypt.org/directory')
                self.assertEqual(issuer['profile'], profile)
                self.assertNotIn('"internal"', result.decode())


if __name__ == '__main__': unittest.main()
