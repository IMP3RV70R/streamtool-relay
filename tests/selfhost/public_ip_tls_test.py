"""Actual pinned proxy TLS without SNI. Fixture trust never implies public ACME."""
import contextlib
import http.client
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time
import unittest
import uuid

ROOT = Path(__file__).resolve().parents[2]


@unittest.skipUnless(os.environ.get('CADDY_TEST_IMAGE'), 'actual pinned proxy is opt-in')
class PublicIpTlsTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix='streamtool-ip-tls-')
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                        '-subj', '/CN=streamtool-tls-fixture', '-addext',
                        'subjectAltName=IP:127.0.0.1,IP:::1,DNS:stream.example.test',
                        '-keyout', str(self.root/'tls.key'), '-out', str(self.root/'tls.crt')],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.context = ssl.create_default_context(cafile=str(self.root/'tls.crt'))

    @contextlib.contextmanager
    def proxy(self, host, default=True):
        config = (ROOT/'infra/selfhost/Caddyfile').read_text()
        if not default: config = config.replace('    default_sni {$TLS_SERVER_NAME}\n', '')
        config = config.replace('{\n', '{\n    https_port 8443\n    http_port 8080\n', 1)
        start = config.index('    tls {'); end = config.index('    header ', start)
        config = config[:start] + '    tls /test/tls.crt /test/tls.key\n' + config[end:]
        (self.root/'Caddyfile').write_text(config)
        name = 'streamtool-ip-tls-' + uuid.uuid4().hex[:12]
        try:
            subprocess.run(['docker', 'run', '--detach', '--name', name, '--cap-drop=ALL',
                            '--security-opt=no-new-privileges', '--user', str(os.getuid())+':'+str(os.getgid()),
                            '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m,mode=1777',
                            '-e', 'XDG_DATA_HOME=/tmp/data', '-e', 'XDG_CONFIG_HOME=/tmp/config', '-p', '127.0.0.1::8443',
                            '-v', str(self.root)+':/test:ro', '-v', str(self.root/'tls.crt')+':/certs/ca.crt:ro',
                            '-e', 'DOMAIN='+host, '-e', 'TLS_SERVER_NAME='+host.strip('[]'),
                            os.environ['CADDY_TEST_IMAGE'], 'caddy', 'run', '--config', '/test/Caddyfile',
                            '--adapter', 'caddyfile'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            port = int(subprocess.check_output(['docker', 'port', name, '8443/tcp'], text=True).strip().rsplit(':', 1)[1])
            deadline = time.monotonic()+20
            while True:
                log = subprocess.check_output(['docker', 'logs', '--tail', '40', name], stderr=subprocess.STDOUT)
                if b'serving initial configuration' in log: break
                if time.monotonic() >= deadline: raise RuntimeError('proxy fixture did not become ready: ' + log.decode())
                time.sleep(.2)
            yield port
        finally:
            subprocess.run(['docker', 'rm', '--force', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def request(self, port, hostname):
        raw = socket.create_connection(('127.0.0.1', port), timeout=3)
        try: connection = self.context.wrap_socket(raw, server_hostname=hostname)
        except Exception: raw.close(); raise
        with connection:
            authority = '['+hostname+']' if ':' in hostname else hostname
            connection.sendall(('GET /v1/installation HTTP/1.1\r\nHost: '+authority+'\r\nConnection: close\r\n\r\n').encode())
            response = http.client.HTTPResponse(connection); response.begin()
            return response.status

    def test_without_default_sni_reproduces_handshake_failure(self):
        with self.proxy('127.0.0.1', default=False) as port:
            with self.assertRaises(ssl.SSLError): self.request(port, '127.0.0.1')

    def test_ipv4_ipv6_and_dns_preserve_certificate_verification(self):
        for host in ('127.0.0.1', '[::1]', 'stream.example.test'):
            with self.subTest(host=host), self.proxy(host) as port:
                self.assertEqual(self.request(port, host.strip('[]')), 404)
                with self.assertRaises(ssl.SSLCertVerificationError): self.request(port, '127.0.0.2')


if __name__ == '__main__': unittest.main()
