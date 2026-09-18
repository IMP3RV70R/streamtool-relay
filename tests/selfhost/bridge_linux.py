"""Actual root Python ↔ UID 65532 Linux Go IPC in a network-disabled container.
Signed catalog/systemd are fixtures; this is not full host update acceptance.
"""
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
from unittest.mock import patch

if sys.platform!='linux' or os.geteuid()!=0 or not Path('/.dockerenv').exists():
    raise SystemExit('Use the isolated Linux test container as root.')
sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'infra/selfhost'))
import updater
from updater_bridge import Bridge, Server

with tempfile.TemporaryDirectory() as temporary:
    directory=Path(temporary);directory.chmod(0o755)
    state=directory/'private';state.mkdir(mode=0o700)
    shared=directory/'socket';shared.mkdir(mode=0o750);os.chown(shared,0,65532)
    path=shared/'control.sock'
    with patch.object(updater,'STATE',state), patch.object(Bridge,'candidate',return_value={
            'digest':'a'*64,'version':'test','notes':'fixture','expires':'2099-01-01T00:00:00Z'}), patch.object(Bridge,'wake'):
        with socket.socket(socket.AF_UNIX,socket.SOCK_STREAM) as listener:
            listener.bind(str(path));os.chown(path,0,65532);path.chmod(0o660);listener.listen(8)
            server=Server(listener,Bridge())
            # The probe makes five separate bounded requests; unauthorized peers
            # and duplicate/oversized framing are checked by updater_bridge_test.
            def serve():
                for _ in range(5):
                    connection,_=listener.accept();server.serve_connection(connection)
            worker=threading.Thread(target=serve,daemon=True);worker.start()
            def unprivileged():
                os.setgroups([]);os.setgid(65532);os.setuid(65532)
            subprocess.run(['/probe','-test.run=^TestBridgeInterop$','-test.v'],
                env={**os.environ,'STREAMTOOL_BRIDGE_INTEROP_SOCKET':str(path)},preexec_fn=unprivileged,check=True,timeout=30)
            worker.join(5)
            assert not worker.is_alive()
    assert len(list((state/'requests').glob('*.json')))==1
print('PASS: actual root Python/UID 65532 Go socket, bidirectional peer checks, fixed protocol, durable exact retry')
