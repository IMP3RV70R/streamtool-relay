package dev.streamtool.app

import com.jcraft.jsch.ChannelExec
import com.jcraft.jsch.HostKey
import com.jcraft.jsch.HostKeyRepository
import com.jcraft.jsch.JSch
import com.jcraft.jsch.Session
import com.jcraft.jsch.UserInfo
import java.io.ByteArrayOutputStream
import java.io.OutputStream
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64

data class SshTarget(val host: String, val port: Int) {
    val identity get() = "[$host]:$port"
    val address get() = if (host.contains(':')) "[$host]:$port" else "$host:$port"
    companion object {
        fun parse(input: String): SshTarget {
            val value = input.trim()
            require(value.length in 1..54)
            val host: String
            val port: String
            if (value.startsWith('[')) {
                val match = requireNotNull(Regex("\\[([^\\]]+)\\](?::([0-9]{1,5}))?").matchEntire(value))
                host = match.groupValues[1]; require(host.contains(':'))
                port = match.groupValues[2].ifEmpty { "22" }
            } else if (value.count { it == ':' } == 1) {
                host = value.substringBefore(':'); port = value.substringAfter(':')
            } else {
                host = value; port = "22"
            }
            require(port.matches(Regex("[0-9]{1,5}")))
            require(host.length <= 45 && (host.matches(Regex("[0-9.]+")) || host.contains(':') && host.matches(Regex("[a-fA-F0-9:]+"))))
            if (!host.contains(':')) {
                val parts = host.split('.')
                require(parts.size == 4 && parts.all { it.toIntOrNull() in 0..255 && (it == "0" || !it.startsWith('0')) })
            }
            val address = try { InetAddress.getByName(host) } catch (_: Exception) { throw IllegalArgumentException("Invalid IP") }
            val number = requireNotNull(port.toIntOrNull()); require(number in 1..65535)
            return SshTarget(requireNotNull(address.hostAddress), number)
        }
    }
}

class SshHostIdentity(val key: ByteArray): Exception() {
    val fingerprint: String get() = "SHA256:" + Base64.getEncoder().withoutPadding().encodeToString(MessageDigest.getInstance("SHA-256").digest(key))
}
class SshIdentityChanged: Exception()

internal fun hostKeyDecision(pinned: ByteArray?, received: ByteArray): Int = when {
    pinned == null -> HostKeyRepository.NOT_INCLUDED
    MessageDigest.isEqual(pinned, received) -> HostKeyRepository.OK
    else -> HostKeyRepository.CHANGED
}

internal fun cabinetOriginForTarget(value: String, target: SshTarget): String {
    val origin = serverOrigin(value, false)
    val uri = java.net.URI(origin)
    require(uri.port == -1)
    require(SshTarget.parse(requireNotNull(uri.host).trim('[', ']')).host == target.host)
    return origin
}

// All commands are bundled, fixed protocol operations. No command accepts UI text.
class SshConnection(private val target: SshTarget, private val pinnedKey: ByteArray?) : AutoCloseable {
    private var session: Session? = null
    fun connect(password: String?) {
        var observed: ByteArray? = null
        var changed = false
        val jsch = JSch()
        jsch.hostKeyRepository = object : HostKeyRepository {
            override fun check(host: String?, key: ByteArray): Int {
                val decision = hostKeyDecision(pinnedKey, key)
                if (decision == HostKeyRepository.NOT_INCLUDED) observed = key.copyOf()
                if (decision == HostKeyRepository.CHANGED) changed = true
                return decision
            }
            override fun add(hostkey: HostKey?, ui: UserInfo?) = Unit
            override fun remove(host: String?, type: String?) = Unit
            override fun remove(host: String?, type: String?, key: ByteArray?) = Unit
            override fun getKnownHostsRepositoryID() = "streamtool-relay pinned host"
            override fun getHostKey(): Array<HostKey> = emptyArray()
            override fun getHostKey(host: String?, type: String?): Array<HostKey> = emptyArray()
        }
        val current = jsch.getSession("root", target.host, target.port)
        session = current
        current.setConfig("StrictHostKeyChecking", "yes")
        current.setConfig("PreferredAuthentications", "password")
        current.setConfig("ForwardAgent", "no")
        current.timeout = 15000
        if (pinnedKey != null && password != null) current.setPassword(password)
        try { current.connect(15000) } catch (e: Exception) {
            close()
            if (changed) throw SshIdentityChanged()
            observed?.let { throw SshHostIdentity(it) }
            throw e
        }
    }
    fun preflight(): String = execute(PREFLIGHT)
    fun cabinetHandoff(): String = execute(HANDOFF)
    fun preparationStatus(): String = execute("python3 /usr/local/libexec/streamtool-installer.py status")
    fun prepare(helper: ByteArray): String {
        require(helper.size in 1..131072)
        val digest = MessageDigest.getInstance("SHA-256").digest(helper).joinToString("") { "%02x".format(it) }
        val bootstrap = """
            import os,sys,hashlib,tempfile,fcntl,json
            from pathlib import Path
            data=sys.stdin.buffer.read(131073)
            if len(data)>131072 or hashlib.sha256(data).hexdigest()!="$digest":raise SystemExit(1)
            root=Path("/var/lib/streamtool-installer")
            root.mkdir(mode=0o700,parents=True,exist_ok=True)
            info=root.lstat()
            if root.is_symlink() or info.st_uid!=0 or info.st_mode&0o077:raise SystemExit(1)
            lock=os.open(root/"lock",os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
            target=Path("/usr/local/libexec/streamtool-installer.py")
            target.parent.mkdir(mode=0o755,parents=True,exist_ok=True)
            if target.is_symlink():raise SystemExit(1)
            if not target.exists() or target.read_bytes()!=data:
                state=root/"state.json"
                if target.exists() and not state.exists():raise SystemExit(1)
                if state.exists() and json.loads(state.read_text()).get("phase") not in ["PREPARED","FAILED"]:raise SystemExit(1)
                fd,name=tempfile.mkstemp(prefix=".installer-",dir=target.parent)
                with os.fdopen(fd,"wb") as output:
                    os.fchmod(output.fileno(),0o700);output.write(data);output.flush();os.fsync(output.fileno())
                os.replace(name,target)
                directory=os.open(target.parent,os.O_DIRECTORY)
                os.fsync(directory);os.close(directory)
            os.close(lock)
            os.execv("/usr/bin/python3",["python3",str(target),"prepare"])
        """.trimIndent()
        require(!bootstrap.contains('\''))
        return execute("python3 -c '$bootstrap'", helper)
    }
    fun installationStatus(): String = execute("python3 /usr/local/libexec/streamtool-installer/installer_application.py status")
    fun install(payload: ByteArray): String {
        require(payload.size in 1..33554432)
        val digest = MessageDigest.getInstance("SHA-256").digest(payload).joinToString("") { "%02x".format(it) }
        val request = org.json.JSONObject().put("address", target.host).toString().toByteArray(Charsets.UTF_8)
        val input = java.nio.ByteBuffer.allocate(4 + request.size + payload.size).putInt(request.size).put(request).put(payload).array()
        val bootstrap = """
            import os,sys,hashlib,zipfile,io,json,stat,tempfile,shutil,fcntl,subprocess,uuid
            from pathlib import Path
            if os.geteuid()!=0:raise SystemExit(1)
            count=int.from_bytes(sys.stdin.buffer.read(4),"big")
            if count<1 or count>1024:raise SystemExit(1)
            request=sys.stdin.buffer.read(count)
            data=sys.stdin.buffer.read(33554433)
            if len(data)>33554432 or hashlib.sha256(data).hexdigest()!="$digest":raise SystemExit(1)
            names={"installer.py","installer_application.py","deploy.py","distribution.json","release-tool-amd64","release-tool-arm64"}
            archive=zipfile.ZipFile(io.BytesIO(data))
            infos=archive.infolist()
            if len(infos)!=len(names) or {i.filename for i in infos}!=names or sum(i.file_size for i in infos)>33554432 or any(stat.S_ISLNK(i.external_attr>>16) for i in infos):raise SystemExit(1)
            root=Path("/var/lib/streamtool-installer/application")
            root.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
            parent_info=root.parent.lstat()
            if root.parent.is_symlink() or parent_info.st_uid!=0 or parent_info.st_mode&0o077:raise SystemExit(1)
            root.mkdir(mode=0o700,parents=True,exist_ok=True)
            info=root.lstat()
            if root.is_symlink() or not root.is_dir() or info.st_uid!=0 or info.st_mode&0o077:raise SystemExit(1)
            lock=os.open(root/"lock",os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
            fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
            target=Path("/usr/local/libexec/streamtool-installer")
            target.parent.mkdir(mode=0o755,parents=True,exist_ok=True)
            if target.is_symlink():raise SystemExit(1)
            same=False
            if target.exists():
                marker=target/".PAYLOAD_SHA256"
                info=target.lstat()
                if info.st_uid!=0 or info.st_mode&0o077 or marker.is_symlink() or not marker.is_file():raise SystemExit(1)
                same=marker.read_text().strip()=="$digest"
                if same and any((target/i.filename).is_symlink() or (target/i.filename).read_bytes()!=archive.read(i) for i in infos):raise SystemExit(1)
                if not same:
                    state=root/"state.json"
                    if state.is_symlink() or not state.exists() or json.loads(state.read_text()).get("phase") not in ["SUCCEEDED","FAILED"]:raise SystemExit(1)
            if not same:
                temporary=Path(tempfile.mkdtemp(prefix=".streamtool-installer-",dir=target.parent))
                for entry in infos:
                    path=temporary/entry.filename
                    with path.open("xb") as output:
                        os.fchmod(output.fileno(),0o600 if entry.filename=="distribution.json" else 0o700)
                        output.write(archive.read(entry));output.flush();os.fsync(output.fileno())
                with (temporary/".PAYLOAD_SHA256").open("x") as output:
                    output.write("$digest");output.flush();os.fsync(output.fileno())
                fd=os.open(temporary,os.O_DIRECTORY);os.fsync(fd);os.close(fd)
                previous=None
                if target.exists():
                    previous=target.with_name(".streamtool-installer-previous-"+str(uuid.uuid4()))
                    os.rename(target,previous)
                os.rename(temporary,target)
                fd=os.open(target.parent,os.O_DIRECTORY);os.fsync(fd);os.close(fd)
                if previous is not None:shutil.rmtree(previous)
            os.close(lock)
            result=subprocess.run(["/usr/bin/python3",str(target/"installer_application.py"),"install"],input=request,timeout=30)
            raise SystemExit(result.returncode)
        """.trimIndent()
        require(!bootstrap.contains('\''))
        return execute("python3 -c '$bootstrap'", input)
    }
    private fun execute(command: String, stdin: ByteArray? = null): String {
        val channel = requireNotNull(session).openChannel("exec") as ChannelExec
        val errors = object : OutputStream() {
            private var count = 0
            override fun write(value: Int) { require(++count <= 32768) }
        }
        channel.setCommand(command); channel.setInputStream(stdin?.inputStream()); channel.setErrStream(errors)
        val input = channel.inputStream
        val output = ByteArrayOutputStream()
        val deadline = System.nanoTime() + 30_000_000_000L
        try {
            channel.connect(10000)
            val buffer = ByteArray(4096)
            while (true) {
                require(System.nanoTime() < deadline) { "SSH operation timed out" }
                while (input.available() > 0) {
                    val count = input.read(buffer)
                    if (count < 0) break
                    require(output.size() + count <= 32768) { "SSH response too large" }
                    output.write(buffer, 0, count)
                }
                if (channel.isClosed && input.available() == 0) break
                Thread.sleep(25)
            }
            require(channel.exitStatus == 0)
            return output.toString("UTF-8")
        } finally { channel.disconnect() }
    }
    override fun close() { session?.disconnect(); session = null }
    companion object {
        private val HANDOFF = """
            python3 - <<'STREAMTOOL_HANDOFF'
            import json,os,sqlite3,stat,base64
            from pathlib import Path
            root=Path('/opt/streamtool')
            def private(path):
                fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW)
                with os.fdopen(fd,'rb') as f:
                    info=os.fstat(f.fileno())
                    if not stat.S_ISREG(info.st_mode) or info.st_uid not in (0,65532) or info.st_mode&0o077:raise ValueError()
                    data=f.read(8193)
                    if len(data)>8192:raise ValueError()
                    return data.decode()
            if os.geteuid()!=0 or root.is_symlink():raise ValueError()
            env=dict(line.split('=',1) for line in private(root/'.env').splitlines() if line and not line.startswith('#'))
            job=Path('/var/lib/streamtool-installer/application/state.json')
            if job.exists() or job.is_symlink():
                if json.loads(private(job)).get('phase')!='SUCCEEDED':raise ValueError()
            database=root/'state/control.sqlite'
            if database.is_symlink():raise ValueError()
            connection=sqlite3.connect(database.as_uri()+'?mode=ro',uri=True)
            try:required=connection.execute('SELECT NOT EXISTS(SELECT 1 FROM installation)').fetchone()[0]==1
            finally:connection.close()
            token=private(root/'secrets/setup_token').strip() if required else ''
            if required and len(base64.b64decode(token,validate=True))!=32:raise ValueError()
            print(json.dumps({'protocol':1,'origin':'https://'+env['DOMAIN'],'setup_token':token}))
            STREAMTOOL_HANDOFF
        """.trimIndent()
        private val PREFLIGHT = """
            python3 - <<'STREAMTOOL_PREFLIGHT'
            import json,os,platform,shutil,socket
            from pathlib import Path
            system={}
            for line in Path('/etc/os-release').read_text().splitlines():
                if '=' in line:
                    k,v=line.split('=',1);system[k]=v.strip('"')
            ports={}
            for port in (80,443,1935):
                s=socket.socket()
                try:s.bind(('0.0.0.0',port));ports[str(port)]=True
                except OSError:ports[str(port)]=False
                finally:s.close()
            memory=int(next(l.split()[1] for l in Path('/proc/meminfo').read_text().splitlines() if l.startswith('MemTotal:')))*1024
            print(json.dumps({'protocol':1,'root':os.geteuid()==0,'os':system.get('ID',''),'version':system.get('VERSION_ID',''),'architecture':platform.machine(),'cpus':os.cpu_count(),'memory_bytes':memory,'disk_free_bytes':shutil.disk_usage('/').free,'ports':ports,'installed':Path('/opt/streamtool/api.env').exists(),'systemd':Path('/run/systemd/system').is_dir(),'installation_job':Path('/var/lib/streamtool-installer/application/state.json').is_file()}))
            STREAMTOOL_PREFLIGHT
        """.trimIndent()
    }
}
