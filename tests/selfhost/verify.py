import importlib.util,sys,tempfile,pathlib,sqlite3,hashlib,json,tarfile,fcntl,subprocess,time
root=pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0,str(root/'infra/selfhost'))
import bootstrap,backup
with tempfile.TemporaryDirectory() as temporary:
 directory=pathlib.Path(temporary)/'install'
 bootstrap.initialize(directory,'stream.example.invalid','streamtool-relay-api:test','streamtool-relay-worker:test','streamtool-relay-proxy:test','bluenviron/mediamtx:1.21.0')
 original=(directory/'secrets/envelope_key').read_bytes()
 try:bootstrap.initialize(directory,'stream.example.invalid','streamtool-relay-api:test','streamtool-relay-worker:test','streamtool-relay-proxy:test','bluenviron/mediamtx:1.21.0')
 except ValueError:pass
 else:raise AssertionError('initialization replaced an installation')
 assert original==(directory/'secrets/envelope_key').read_bytes()
 for name in ('api','edge','agent','controller'):
  subprocess.run(['openssl','verify','-x509_strict','-CAfile',str(directory/'certs/ca.crt'),str(directory/'certs'/f'{name}.crt')],check=True,stdout=subprocess.DEVNULL)
 database=sqlite3.connect(directory/'state/control.sqlite')
 database.create_function('uuid',0,lambda:'00000000-0000-4000-8000-000000000001')
 for migration in sorted((root/'backend/sqlite-migrations').glob('*.up.sql')):database.executescript(migration.read_text())
 database.execute("INSERT INTO accounts(name) VALUES('owner')")
 database.execute("INSERT INTO users(id,account_id,email,password_hash,password_salt) SELECT 'owner',id,'historical@example.invalid',x'01',x'02' FROM accounts")
 database.execute("INSERT INTO installation VALUES(1,'owner')")
 database.execute("INSERT INTO owner_mfa VALUES('owner',x'0304',100)")
 database.execute("INSERT INTO owner_recovery VALUES(x'05','owner')")
 database.execute("INSERT INTO user_sessions(token_hash,user_id,expires_at) VALUES(x'06','owner','2099-01-01T00:00:00Z')")
 database.execute("INSERT INTO auth_enrollment VALUES(1,x'07','owner',x'01',x'02',x'08',x'09','2099-01-01T00:00:00Z')")
 database.execute("INSERT INTO update_authorizations(request_id,release_digest,user_id,session_hash,expires_at) VALUES(?,?,'owner',?,'2099-01-01T00:00:00Z')",('11111111-1111-4111-8111-111111111111','a'*64,bytes(32)))
 database.commit();database.close()
 destination=pathlib.Path(temporary)/'backup.tar.gz'
 backup.snapshot(directory,destination)
 assert destination.stat().st_mode&0o777==0o600
 with tarfile.open(destination) as archive:
  manifest=json.load(archive.extractfile('manifest.json'))
  assert hashlib.sha256(archive.extractfile('secrets/envelope_key').read()).hexdigest()==manifest['secrets/envelope_key']
  assert archive.extractfile('secrets/envelope_key').read()==original
  restored=pathlib.Path(temporary)/'restored.sqlite'
  restored.write_bytes(archive.extractfile('control.sqlite').read())
 database=sqlite3.connect(restored)
 backup.revoke_restored_auth(database);database.commit();database.close()
 database=sqlite3.connect(restored)
 for table in ('user_sessions','auth_enrollment','owner_recovery'):
  assert database.execute('SELECT count(*) FROM '+table).fetchone()[0]==0
 assert database.execute('SELECT state FROM update_authorizations').fetchone()[0]=='CANCELLED'
 secret,last_step=database.execute('SELECT secret,last_step FROM owner_mfa').fetchone()
 assert secret==bytes.fromhex('0304') and last_step>=int(time.time())//30
 assert database.execute('SELECT password_hash,email FROM users').fetchone()==(bytes.fromhex('01'),'historical@example.invalid')
 database.close()
 with (directory/'state/control.sqlite.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  try:backup.snapshot(directory,pathlib.Path(temporary)/'blocked.tar.gz')
  except BlockingIOError:pass
  else:raise AssertionError('backup ignored the control-process lock')
 print('PASS: private consistent backup, exclusive lock, restored credentials revoked without rotating authenticator or keys')
