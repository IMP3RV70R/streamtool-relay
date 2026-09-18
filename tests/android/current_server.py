#!/usr/bin/env python3
"""Run the optional Android contract test against the owned local control stand.
Owner credentials travel only via stdin into private debug-app storage.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
from urllib.parse import urlsplit

parser = argparse.ArgumentParser()
parser.add_argument('--origin', required=True)
parser.add_argument('--owner-fixture', type=Path, required=True)
args = parser.parse_args()
origin = urlsplit(args.origin)
if origin.scheme != 'http' or origin.hostname != '127.0.0.1' or not origin.port or origin.path or origin.query or origin.fragment or origin.username:
    raise SystemExit('Use the owned local control stand HTTP origin, without credentials/path.')
if args.owner_fixture.stat().st_mode & 0o077:
    raise SystemExit('Owner fixture must be private (0600/0400).')
owner = json.loads(args.owner_fixture.read_text())
fixture = {key: owner[key] for key in ('password', 'totp_secret')}
fixture['origin'] = args.origin
sdk = Path(os.environ['ANDROID_HOME'])
adb = str(sdk / 'platform-tools/adb')
root = Path(__file__).resolve().parents[2]

def run(*arguments, **kwargs):
    return subprocess.run([adb, *arguments], check=True, stdout=subprocess.DEVNULL, **kwargs)

# Gradle connected tests uninstall their packages; use direct instrumentation so
# the private fixture is present at the instant the real-server test starts.
run('install', '-r', str(root / 'apps/android/app/build/outputs/apk/debug/app-debug.apk'))
run('install', '-r', str(root / 'apps/android/app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk'))
port = 'tcp:' + str(origin.port)
existing = subprocess.check_output([adb, 'reverse', '--list'], text=True)
owned_reverse = not any(port in line.split()[1:2] for line in existing.splitlines())
if not owned_reverse and not any(line.split()[-2:] == [port, port] for line in existing.splitlines()):
    raise SystemExit('Conflicting adb reverse mapping; refusing to replace it.')
run('reverse', port, port)
try:
    run('shell', '-T', "run-as dev.streamtool.app sh -c 'mkdir -p files; umask 077; cat > files/integration-owner.json'", input=json.dumps(fixture).encode())
    result = subprocess.run([adb, 'shell', 'am', 'instrument', '-w', '-e', 'class', 'dev.streamtool.app.CurrentServerTest', 'dev.streamtool.app.test/androidx.test.runner.AndroidJUnitRunner'], check=True, capture_output=True, text=True)
    output = result.stdout + result.stderr
    if 'OK (1 test)' not in output or 'AssumptionViolated' in output or 'FAILURES' in output:
        # Safe test diagnostics never intentionally contain fixture passwords/keys.
        for secret in fixture.values():
            if secret: output = output.replace(secret, '[redacted]')
        print(output)
        raise SystemExit('Real-server Android integration failed or skipped.')
    print('PASS: Android real-server password/TOTP, cookie authority, output CRUD/generation and logout revocation')
finally:
    run('shell', '-T', "run-as dev.streamtool.app sh -c 'rm -f files/integration-owner.json'")
    if owned_reverse: run('reverse', '--remove', port)
