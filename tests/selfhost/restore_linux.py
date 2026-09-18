"""Real snapshot restoration in an isolated Linux container, without Docker/systemd.
Do not run on the host: fixed system paths are intentionally inside the container.
"""
import fcntl
import grp
import os
from pathlib import Path
import pwd
import sqlite3
import sys
import tempfile
import uuid

if sys.platform != 'linux' or os.geteuid() != 0 or not Path('/.dockerenv').exists():
    raise SystemExit('Use the isolated Linux test container as root.')
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'infra/selfhost'))
import backup

try:
    pwd.getpwnam('streamtool-agent')
except KeyError:
    with Path('/etc/passwd').open('a') as output:
        output.write('streamtool-agent:x:61001:61001::/nonexistent:/usr/sbin/nologin\n')
    with Path('/etc/group').open('a') as output:
        output.write('streamtool-agent:x:61001:\n')
Path('/etc/streamtool').mkdir(exist_ok=True)
Path('/var/lib/streamtool').mkdir(exist_ok=True)
fence = Path('/var/lib/streamtool/fence.json')
fence.write_text('99\n')
repository = Path(__file__).resolve().parents[2]
with tempfile.TemporaryDirectory() as temporary:
    root = Path(temporary) / 'application'
    for directory in ('state','secrets','certs'):
        (root / directory).mkdir(parents=True)
    original_key = os.urandom(32)
    (root / 'secrets/envelope_key').write_bytes(original_key)
    for name in ('ca.crt','ca.key','api.crt','api.key','controller.crt','controller.key','agent.crt','agent.key','edge.crt','edge.key'):
        (root / 'certs' / name).write_bytes(os.urandom(32))
    for name in ('keyring.json','media-node.json','api.env','.env','node.env','worker-network.env'):
        (root / name).write_text('old coherent configuration\n')
    database = sqlite3.connect(root / 'state/control.sqlite')
    database.create_function('uuid',0,lambda:str(uuid.uuid4()))
    for migration in sorted((repository / 'backend/sqlite-migrations').glob('*.up.sql')):
        database.executescript(migration.read_text())
    database.execute("INSERT INTO accounts(name) VALUES('owner')")
    database.execute("INSERT INTO users(id,account_id,email,password_hash,password_salt) SELECT 'owner',id,'historical@example.invalid',x'01',x'02' FROM accounts")
    database.execute("INSERT INTO installation VALUES(1,'owner')")
    database.execute("INSERT INTO owner_mfa VALUES('owner',x'0304',100)")
    database.execute("INSERT INTO owner_recovery VALUES(x'05','owner')")
    database.execute("INSERT INTO user_sessions(token_hash,user_id,expires_at) VALUES(x'06','owner','2099-01-01T00:00:00Z')")
    database.commit()
    database.close()
    destination = Path(temporary) / 'snapshot.tar.gz'
    backup.snapshot(root,destination)
    # Simulate a damaged target DB and a newer Agent watermark. Rollback must not
    # require successfully backing up the already broken target database.
    (root / 'state/control.sqlite').write_bytes(b'damaged replacement database')
    fence.write_text('101\n')
    with (root / 'state/control.sqlite.lock').open('a') as lock:
        fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
        try:
            backup.restore_snapshot(root,destination)
        except BlockingIOError:
            pass
        else:
            raise AssertionError('restore ignored exclusive control ownership')
    for repeat in range(2):
        backup.restore_snapshot(root,destination)
        database = sqlite3.connect(root / 'state/control.sqlite')
        assert database.execute('PRAGMA integrity_check').fetchone()[0] == 'ok'
        assert database.execute('SELECT name FROM accounts').fetchone()[0] == 'owner'
        for table in ('user_sessions','owner_recovery','auth_enrollment'):
            assert database.execute('SELECT count(*) FROM '+table).fetchone()[0] == 0
        assert database.execute('SELECT secret FROM owner_mfa').fetchone()[0] == bytes.fromhex('0304')
        database.close()
        assert (root / 'secrets/envelope_key').read_bytes() == original_key
        assert int(fence.read_text()) == 101
        assert (root / 'state/control.sqlite').stat().st_mode & 0o777 == 0o600
        assert (root / 'state/control.sqlite').stat().st_uid == 65532
        assert not (root / 'state/control.sqlite-wal').exists()
        assert not (root / 'state/control.sqlite-shm').exists()
print('PASS: actual Linux SQLite snapshot restoration, damaged target, repeatability, exclusive ownership, key preservation, revoked credentials and monotonic Agent fence')
