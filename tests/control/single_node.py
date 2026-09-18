#!/usr/bin/env python3
"""Real single-node acceptance, including controller/agent/worker/edge recovery."""
import base64
import hmac
import struct
import hashlib
import secrets
import json
import os
import subprocess
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path
import http.cookiejar

COMPOSE = ["docker", "compose", "-f", "compose.control.yml"]
BASE = "http://127.0.0.1:" + os.getenv("CONTROL_API_PORT", "18080")
TOKEN = open("infra/local/secrets/admin_token").read().strip()
NODE = os.getenv("CONTROL_TEST_NODE_ID", "00000000-0000-4000-8000-000000000002")
PUBLISHER = "streamtool-control-test-publisher"
CREATED_STREAMS = []
MEDIA = {"width": int(os.getenv("CONTROL_TEST_WIDTH", "1280")), "height": int(os.getenv("CONTROL_TEST_HEIGHT", "720")), "fps_num": int(os.getenv("CONTROL_TEST_FPS", "30")), "fps_den": 1, "video_kbps": int(os.getenv("CONTROL_TEST_VIDEO_KBPS","3000")), "audio_kbps": 160}


def run(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()


def api(method, path, data=None, token=TOKEN, session=None, expected=200):
    body = None if data is None else json.dumps(data).encode()
    headers = {"Content-Type": "application/json", "X-Streamtool": "1"}
    if token:
        headers["Authorization"] = "Bearer " + token
    if session:
        headers["Cookie"] = "streamtool_session=" + session
    try:
        with urllib.request.urlopen(urllib.request.Request(BASE + path, body, headers, method=method), timeout=5) as r:
            code, body = r.status, r.read()
    except urllib.error.HTTPError as e:
        code, body = e.code, e.read()
    assert code == expected, f"{method} {path}: HTTP {code}, expected {expected}"
    return json.loads(body) if body else None


def wait(description, fn, timeout=90):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                print("PASS:", description, flush=True)
                return result
        except (AssertionError, OSError, subprocess.CalledProcessError):
            pass
        time.sleep(2)
    raise AssertionError("timeout: " + description)


def worker_id():
    return run("docker", "ps", "-q", "--filter", "label=streamtool.node=" + NODE)


def bytes_received():
    with urllib.request.urlopen("http://127.0.0.1:" + os.getenv("CONTROL_RECEIVER_API_PORT", "19998") + "/v3/rtmpconns/list", timeout=3) as r:
        return sum(item.get("bytesReceived", 0) for item in json.load(r)["items"])


def receiver_connections(secret):
    with urllib.request.urlopen("http://127.0.0.1:" + os.getenv("CONTROL_RECEIVER_API_PORT", "19998") + "/v3/rtmpconns/list", timeout=3) as r:
        return sorted(item["id"] for item in json.load(r)["items"] if item.get("state") == "publish" and item.get("path") == "live/" + secret)


def receiver_byte_counts():
    with urllib.request.urlopen("http://127.0.0.1:" + os.getenv("CONTROL_RECEIVER_API_PORT", "19998") + "/v3/rtmpconns/list",timeout=3) as r:
        return {item['path']:item.get('bytesReceived',0) for item in json.load(r)['items'] if item.get('state')=='publish'}


def assert_slate(secret, expected=True):
    # Decode the actual RTMP output and inspect RGB pixels (not just byte counters).
    reference = subprocess.check_output(COMPOSE + ["run", "--rm", "--no-deps",
        "--volume", os.path.abspath("backend/assets/offline.png") + ":/tmp/reference.png:ro",
        "publisher", "-q", "filesrc", "location=/tmp/reference.png", "!", "pngdec",
        "!", "videoconvert", "!", "videoscale", "!", "video/x-raw,format=RGB,width=16,height=9",
        "!", "fdsink", "fd=1"], stderr=subprocess.DEVNULL, timeout=25)
    assert len(reference) == 16 * 9 * 3, "invalid decoded slate reference"
    name = "streamtool-fallback-probe"
    subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        command = COMPOSE + ["run", "-d", "--no-deps", "--name", name, "publisher",
            "rtmpsrc", "location=rtmp://receiver:1935/live/" + secret,
            "!", "flvdemux", "name=d", "d.", "!", "video/x-h264", "!", "queue", "!", "h264parse", "!", "avdec_h264",
            "!", "video/x-raw,width=" + str(MEDIA["width"]) + ",height=" + str(MEDIA["height"]) + ",framerate=" + str(MEDIA["fps_num"]) + "/1",
            "!", "videoconvert", "!", "videoscale", "!", "video/x-raw,format=RGB,width=16,height=9",
            "!", "filesink", "location=/tmp/frame.rgb", "buffer-mode=unbuffered", "async=false", "sync=false",
            "d.", "!", "audio/mpeg", "!", "queue", "!", "aacparse", "!", "avdec_aac", "!", "audioconvert",
            "!", "audio/x-raw,format=S16LE", "!", "filesink", "location=/tmp/audio.raw", "buffer-mode=unbuffered", "async=false", "sync=false"]
        result = subprocess.run(command, timeout=25, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
        assert result.returncode == 0, result.stdout.replace(secret, "[REDACTED]")
        wait("decoded video and audio available", lambda: subprocess.run(
            ["docker", "exec", name, "sh", "-c", "test -s /tmp/frame.rgb && test -s /tmp/audio.raw"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0, timeout=20)
        import tempfile
        import struct
        # A newly attached RTMP reader may start at a cached GOP. Check growing,
        # recent decoded output, not the first cached picture or all historical audio.
        previous_size = 0
        stable = 0
        latest = None
        with tempfile.TemporaryDirectory() as tmp:
            def fresh_media():
                nonlocal previous_size, stable, latest
                run("docker", "cp", name + ":/tmp/frame.rgb", tmp + "/frame.rgb")
                data = open(tmp + "/frame.rgb", "rb").read()
                run("docker", "cp", name + ":/tmp/audio.raw", tmp + "/audio.raw")
                audio = open(tmp + "/audio.raw", "rb").read()
                frame_bytes = 16 * 9 * 3
                end = len(data) // frame_bytes * frame_bytes
                if end < frame_bytes or len(audio) < 8192 or end <= previous_size:
                    stable = 0
                    return False
                previous_size = end
                frame = data[end-frame_bytes:end]
                peak = max(abs(sample[0]) for sample in struct.iter_unpack("<h", audio[len(audio)//2*2-8192:len(audio)//2*2]))
                error = sum(abs(a-b) for a,b in zip(frame, reference)) / frame_bytes
                slate = error <= 20
                latest = {"image_mean_error": error, "audio_peak": peak}
                if expected and os.getenv("CONTROL_TEST_FALLBACK_FILE"):
                    frames = [data[i:i+frame_bytes] for i in range(max(0,end-60*frame_bytes),end,frame_bytes)]
                    motion = max((max(abs(a-b) for a,b in zip(frames[0],f)) for f in frames[1:]),default=0)
                    latest["motion_peak"] = motion
                    if motion < 4: return False
                stable = stable + 1 if slate == expected and (peak <= 64) == expected else 0
                return stable >= 2
            try:
                wait("fresh decoded video and audio match expected source", fresh_media, timeout=40)
            except AssertionError as error:
                raise AssertionError(f"expected slate={expected}, latest={latest}") from error
        print("PASS: decoded " + (("looped video fallback" if os.getenv("CONTROL_TEST_FALLBACK_FILE") else "static fallback image") if expected else "restored source"), flush=True)
    finally:
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def publish(stream):
    subprocess.run(["docker", "rm", "-f", PUBLISHER], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    base = json.loads(run("docker", "compose", "-f", "compose.yml", "config", "--format", "json"))
    command = base["services"]["publisher"]["command"]
    uri_index = next(i for i, argument in enumerate(command) if argument.startswith("uri="))
    command = ["video/x-raw,format=I420,width=" + str(MEDIA["width"]) + ",height=" + str(MEDIA["height"]) + ",framerate=" + str(MEDIA["fps_num"]) + "/1" if arg.startswith("video/x-raw,width=") else arg for arg in command]
    command = ["key-int-max=" + str(2 * MEDIA["fps_num"]) if arg.startswith("key-int-max=") else "bitrate=" + str(MEDIA["video_kbps"]) if arg=="bitrate=3000" else arg for arg in command]
    # Encoder latency must not block the other branch at the MPEG-TS mux.
    for element in ("x264enc", "audioconvert"):
        index = command.index(element)
        command[index:index] = ["queue", "max-size-time=3000000000", "max-size-bytes=0", "max-size-buffers=0", "!"]
    # Delay initial AAC buffers so MPEG-TS declares both tracks before sending
    # its first PMT; delayed B-frame video must not appear as an undeclared PID.
    index = command.index("mux.")
    command[index:index] = ["queue", "min-threshold-time=500000000", "max-size-time=3000000000", "max-size-bytes=0", "max-size-buffers=0", "!"]
    command.insert(command.index("x264enc")+1, "threads=2")
    if os.getenv("CONTROL_TEST_BFRAMES"):
        command.insert(command.index("x264enc") + 1, "bframes=" + os.environ["CONTROL_TEST_BFRAMES"])
    uri_index = next(i for i, argument in enumerate(command) if argument.startswith("uri="))
    command[uri_index] = "uri=srt://edge:8890?streamid=publish:" + stream["stream"]["id"] + ":publisher:" + stream["ingest_key"] + "&pkt-size=1316"
    run(*COMPOSE, "run", "-d", "--no-deps", "--name", PUBLISHER, "publisher", *command)


def measure_worker(label):
    if os.getenv("CONTROL_TEST_BENCHMARK") != "1": return
    worker = worker_id()
    def ticks():
        data = run("docker", "exec", worker, "/usr/local/bin/stream-worker", "runtime-io", "read-stat")
        fields = data[data.rindex(")")+2:].split()
        return int(fields[11]) + int(fields[12])
    # The fixture shares the worker kernel; do not ship getconf just for probes.
    hz = int(subprocess.check_output(COMPOSE + ["run", "--rm", "--no-deps", "--entrypoint", "getconf", "publisher", "CLK_TCK"], text=True, stderr=subprocess.DEVNULL).strip())
    before, started = ticks(), time.monotonic()
    time.sleep(10)
    used, elapsed = ticks()-before, time.monotonic()-started
    memory = int(run("docker", "exec", worker, "/usr/local/bin/stream-worker", "runtime-io", "read-memory"))
    print("MEASURE: " + label + " worker CPU %.3f cores; memory %.1f MiB; %.1fs sample" % (used/hz/elapsed, memory/(1<<20), elapsed), flush=True)


def main():
    wait("API ready", lambda: urllib.request.urlopen(BASE + "/healthz", timeout=3).status == 200)
    api("POST", "/v1/accounts", {"name": "denied"}, token=None, expected=401)
    api("POST", "/v1/edge/observations", {}, token=TOKEN, expected=401)
    no_cert = subprocess.run(COMPOSE + ["exec", "-T", "agent", "wget", "--no-check-certificate", "-qO-", "https://agent:8443/v1/workers"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    assert no_cert.returncode != 0, "Agent accepted a request without client certificate"
    credentials_path=Path('.artifacts/control-owner.json')
    if credentials_path.exists(): credentials=json.loads(credentials_path.read_text())
    else:
        credentials={"password":secrets.token_urlsafe(32)}
        credentials_path.write_text(json.dumps(credentials)); credentials_path.chmod(0o600)
    jar=http.cookiejar.CookieJar()
    opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
    required=api("GET","/v1/auth/setup",token=None)["required"]
    headers={"Content-Type":"application/json","X-Streamtool":"1"}
    def totp(secret):
        key=base64.b32decode(secret+'='*((8-len(secret)%8)%8))
        digest=hmac.new(key,struct.pack('>Q',int(time.time())//30),hashlib.sha1).digest()
        offset=digest[-1]&15
        return '%06d'%((struct.unpack('>I',digest[offset:offset+4])[0]&0x7fffffff)%1000000)
    if required:
        headers["X-Setup-Token"]=Path('infra/local/secrets/setup_token').read_text().strip()
        with opener.open(urllib.request.Request(BASE+'/v1/auth/setup',json.dumps({'password':credentials['password']}).encode(),headers,method='POST'),timeout=15) as response: enrollment=json.load(response)
        credentials['totp_secret']=enrollment['secret']
        credentials_path.write_text(json.dumps(credentials));credentials_path.chmod(0o600)
        with opener.open(urllib.request.Request(BASE+'/v1/auth/setup/confirm',json.dumps({'enrollment_token':enrollment['enrollment_token'],'code':totp(enrollment['secret'])}).encode(),headers,method='POST'),timeout=15) as response: assert response.status==200
    else:
        assert credentials.get('totp_secret'),'Missing private test TOTP fixture'
        with opener.open(urllib.request.Request(BASE+'/v1/auth/login',json.dumps({'password':credentials['password'],'code':totp(credentials['totp_secret'])}).encode(),headers,method='POST'),timeout=15) as response: assert response.status==200
    owner_token=next(cookie.value for cookie in jar if cookie.name=='streamtool_session')
    provision=api("POST","/v1/me/source",token=None,session=owner_token)
    sid=provision["source_id"]
    CREATED_STREAMS.append(sid)
    key_path=Path('.artifacts/control-ingest-key')
    if provision.get('ingest_key'): key_path.write_text(provision['ingest_key']); key_path.chmod(0o600)
    stream={"stream":{"id":sid},"ingest_key":key_path.read_text().strip()}
    def previous_run_stopped():
        request = urllib.request.Request(BASE+f"/v1/streams/{sid}/status",headers={"Authorization":"Bearer "+TOKEN})
        try:
            with urllib.request.urlopen(request,timeout=5) as response: state=json.load(response)
        except urllib.error.HTTPError as error:
            if error.code==404: return True
            raise
        return state["session"]["phase"] in ("ENDED","FAILED")
    wait("previous test run has no active session", previous_run_stopped)
    current_profile=api("GET","/v1/me/source/media",token=None,session=owner_token)
    api("PUT", "/v1/me/source/media", dict(MEDIA, generation=current_profile["generation"]), token=None, session=owner_token)
    initial_slate=api("GET","/v1/me/source/slate",token=None,session=owner_token)
    if initial_slate['forced'] or not initial_slate['on_source_loss']:
        api("PUT","/v1/me/source/slate",dict(initial_slate,forced=False,on_source_loss=True),token=None,session=owner_token)

    if not os.getenv("CONTROL_TEST_FALLBACK_FILE"):
        existing_asset=api("GET","/v1/me/source/fallback",token=None,session=owner_token)
        if existing_asset and existing_asset['kind']!='default':
            api("DELETE","/v1/me/source/fallback?generation="+str(existing_asset['generation']),token=None,session=owner_token,expected=204)
    if os.getenv("CONTROL_TEST_FALLBACK_FILE"):
        data = open(os.environ["CONTROL_TEST_FALLBACK_FILE"],"rb").read()
        asset = api("GET","/v1/me/source/fallback",token=None,session=owner_token)
        request = urllib.request.Request(BASE + "/v1/me/source/fallback?generation="+str(asset["generation"] if asset else 0),data,{"Content-Type":"video/mp4","X-Streamtool":"1","Cookie":"streamtool_session="+owner_token},method="PUT")
        with urllib.request.urlopen(request,timeout=30) as response: assert response.status==204
    collection_path = "/v1/me/source/outputs"
    destination_secret = "managed-" + uuid.uuid4().hex
    old_outputs=api("GET",collection_path,token=None,session=owner_token)
    for old in old_outputs:
        api("DELETE",collection_path+"/"+old['id'],{"generation":old['generation']},token=None,session=owner_token,expected=204)
    dest=api("POST",collection_path,{"name":"receiver","endpoint":"rtmp://receiver:1935/live","secret":destination_secret,"enabled":True,"generation":0},token=None,session=owner_token,expected=201)
    outputs_path=collection_path+"/"+dest['id']
    def current_output():
        return next(d for d in api("GET",collection_path,token=None,session=owner_token) if d['id']==dest['id'])
    api("POST", "/v1/edge/auth", {"action": "publish", "protocol": "srt", "path": sid, "id": str(uuid.uuid4()), "password": "invalid"}, token=None, expected=404)
    publish(stream)
    live = lambda: api("GET", f"/v1/streams/{sid}/status")["status"] == "LIVE"
    wait("publish automatically reaches LIVE", live)
    wait("RTMP receiver gets media", lambda: bytes_received() > 0)
    measure_worker("live")
    if os.getenv("CONTROL_TEST_MEDIA_ONLY") != "1":
        initial = worker_id()
        assert initial and "\n" not in initial, "expected exactly one worker"
        public = run("docker", "inspect", initial)
        assert stream["ingest_key"] not in public
        assert destination_secret not in public

        # RTMP handshake bytes precede an authenticated publish path. Capture
        # the baseline only once the receiver has that actual connection.
        stable_connection=wait("primary receiver connection established",lambda: receiver_connections(destination_secret))
        extras=[]
        for index in range(1,8):
            secret="managed-"+uuid.uuid4().hex
            created=api("POST",collection_path,{"name":"output-"+str(index),"endpoint":"rtmp://receiver:1935/live","secret":secret,"enabled":True,"generation":0},token=None,session=owner_token,expected=201)
            extras.append((created['id'],secret))
        api("POST",collection_path,{"name":"ninth","endpoint":"rtmp://receiver:1935/live","secret":"unused","enabled":True,"generation":0},token=None,session=owner_token,expected=409)
        wait("eight independent outputs stream",lambda: sum(d['state']=='STREAMING' for d in api("GET",f"/v1/streams/{sid}/status")['session']['destinations'])==8)
        wait("eight receiver connections established",lambda: all(receiver_connections(secret) for _,secret in extras))
        all_connections={secret:receiver_connections(secret) for _,secret in extras}
        all_connections[destination_secret]=stable_connection
        slate=api("GET","/v1/me/source/slate",token=None,session=owner_token)
        slate=api("PUT","/v1/me/source/slate",{"on_source_loss":True,"forced":True,"generation":slate['generation']},token=None,session=owner_token)
        wait("common fallback reaches eight outputs",lambda: api("GET",f"/v1/streams/{sid}/status")['session']['fallback_forced'])
        assert_slate(extras[-1][1])
        assert all(receiver_connections(secret)==connections for secret,connections in all_connections.items()), "RTMP connection changed after forced fallback"
        api("PUT","/v1/me/source/slate",{"on_source_loss":True,"forced":False,"generation":slate['generation']},token=None,session=owner_token)
        wait("eight outputs return to source",live)
        assert_slate(extras[-1][1],expected=False)
        assert all(receiver_connections(secret)==connections for secret,connections in all_connections.items()), "RTMP connection changed after source return"
        assert worker_id()==initial and receiver_connections(destination_secret)==stable_connection
        broken,broken_secret=extras[-1]
        healthy_connections={secret:receiver_connections(secret) for _,secret in extras[:-1]}
        api("PUT",collection_path+"/"+broken,{"name":"output-7","endpoint":"rtmp://receiver:1936/live","secret":"","enabled":True,"generation":1},token=None,session=owner_token,expected=204)
        wait("failed destination is isolated",lambda: any(d['id']==broken and d['state']!='STREAMING' for d in api("GET",f"/v1/streams/{sid}/status")['session']['destinations']))
        before=receiver_byte_counts()
        time.sleep(5)
        after=receiver_byte_counts()
        assert all(after.get('live/'+secret,0)>before.get('live/'+secret,0)+10000 for secret in [destination_secret,*healthy_connections]), "failed output stalled peer media"
        print("PASS: seven healthy outputs continue receiving media during peer failure",flush=True)
        assert receiver_connections(destination_secret)==stable_connection
        assert all(receiver_connections(secret)==connections for secret,connections in healthy_connections.items())
        assert worker_id()==initial
        api("PUT",collection_path+"/"+broken,{"name":"output-7","endpoint":"rtmp://receiver:1935/live","secret":"","enabled":True,"generation":2},token=None,session=owner_token,expected=204)
        wait("failed output recovers without restarting peers",lambda: len(receiver_connections(broken_secret))==1)
        assert receiver_connections(destination_secret)==stable_connection
        for output_id,_ in extras:
            item=next(d for d in api("GET",collection_path,token=None,session=owner_token) if d['id']==output_id)
            api("DELETE",collection_path+"/"+output_id,{"generation":item['generation']},token=None,session=owner_token,expected=204)
        wait("removing extra outputs retains primary",lambda: len(api("GET",f"/v1/streams/{sid}/status")['session']['destinations'])==1)
        assert receiver_connections(destination_secret)==stable_connection
        if os.getenv('CONTROL_TEST_MULTISTREAM_ONLY')=='1':
            run('docker','rm','-f',PUBLISHER)
            wait('multistream source disconnection permits stop',lambda: api('GET',f'/v1/streams/{sid}/status')['can_stop'])
            session_id=api('GET',f'/v1/streams/{sid}/status')['session']['session_id']
            api('POST',f'/v1/sessions/{session_id}/stop',{},expected=202)
            wait('multistream completion removes worker',lambda: not worker_id())
            print('Multistream acceptance passed',flush=True)
            return
        api("POST", outputs_path + "/retry", {"generation": current_output()["generation"]}, token=None, session=owner_token, expected=204)
        wait("single output retry reaches new generation", lambda: any(d["id"] == dest["id"] and d["generation"] >= 2 and d["state"] == "STREAMING" for d in api("GET", f"/v1/streams/{sid}/status")["session"]["destinations"]))
        assert worker_id() == initial
        run(*COMPOSE, "restart", "api", "agent")
        wait("API/Agent restart adopts worker", live)
        assert worker_id() == initial, "control restart interrupted healthy media"
        before = bytes_received()
        wait("media continues after adoption", lambda: bytes_received() > before)
        run(*COMPOSE, "stop", "api")
        before = bytes_received()
        wait("control process and SQLite shutdown preserves media", lambda: bytes_received() > before)
        assert worker_id() == initial
        run(*COMPOSE, "start", "api")
        wait("control and SQLite restart restores observation", live)
        assert worker_id() == initial
        run("docker", "kill", initial)
        wait("worker crash creates replacement", lambda: worker_id() and worker_id() != initial)
        wait("worker crash recovers media status", live)
        run(*COMPOSE, "restart", "edge")
        # Reconnect as a new publisher after edge loss; real clients do this automatically.
        time.sleep(7)
        publish(stream)
        # Do not accept the old pre-restart LIVE projection as recovery evidence.
        time.sleep(4)
        wait("edge restart recovers session and media", live)
        before = bytes_received()
        wait("edge recovery delivers fresh media", lambda: bytes_received() > before)
        time.sleep(4)
        snapshot = api("GET", f"/v1/streams/{sid}/status")
        session_id = snapshot["session"]["session_id"]
        assert not snapshot["can_stop"], "live source allowed stop"
        api("POST", f"/v1/sessions/{session_id}/stop", {}, expected=409)
        run("docker", "rm", "-f", PUBLISHER)
        wait("source disconnection permits explicit stop", lambda: api("GET", f"/v1/streams/{sid}/status")["can_stop"])
        api("POST", f"/v1/sessions/{session_id}/stop", {}, expected=202)
        wait("operator stop removes worker", lambda: not worker_id())
        wait("operator stop reaches STOPPED", lambda: api("GET", f"/v1/streams/{sid}/status")["status"] == "STOPPED")
        time.sleep(5)
        assert not worker_id(), "stopped session resurrected without a publisher"
        time.sleep(12)
        publish(stream)
        wait("new publisher creates a new live session", live)
        current = worker_id()
        session_id = api("GET", f"/v1/streams/{sid}/status")["session"]["session_id"]
    else:
        current = worker_id()
        session_id = api("GET", f"/v1/streams/{sid}/status")["session"]["session_id"]
    connections = wait("destination connection established", lambda: receiver_connections(destination_secret))

    if os.getenv("CONTROL_TEST_MEDIA_ONLY") != "1":
        width = MEDIA["width"]
        try:
            MEDIA["width"] = width // 2
            publish(stream)
            time.sleep(5)  # Expire the previous decoded source observation.
            wait("different source resolution is normalized",live)
            assert not api("GET",f"/v1/streams/{sid}/status")["can_stop"]
            assert receiver_connections(destination_secret) == connections
        finally:
            MEDIA["width"] = width
        publish(stream)
        wait("compatible source clears media error", lambda: live() and not api("GET", f"/v1/streams/{sid}/status")["session"].get("input_error"))
        assert receiver_connections(destination_secret) == connections

    slate = api("GET", "/v1/me/source/slate", token=None, session=owner_token)
    slate = api("PUT", "/v1/me/source/slate", {"on_source_loss": True, "forced": True, "generation": slate["generation"]}, token=None, session=owner_token)
    wait("forced slate overrides live source", lambda: (lambda state: state["fallback_forced"] and state["input_live"])(api("GET", f"/v1/streams/{sid}/status")["session"]))
    forced = api("GET", f"/v1/streams/{sid}/status")["session"]
    assert forced["fallback_active"] and forced["input_live"], "forced slate hid live source state"
    assert worker_id() == current
    assert receiver_connections(destination_secret) == connections, "forced slate reconnected RTMP output"
    measure_worker("fallback")
    assert_slate(destination_secret)
    slate = api("PUT", "/v1/me/source/slate", {"on_source_loss": True, "forced": False, "generation": slate["generation"]}, token=None, session=owner_token)
    wait("manual slate returns to source", live)
    assert_slate(destination_secret, expected=False)
    assert worker_id() == current
    assert receiver_connections(destination_secret) == connections

    slate = api("PUT", "/v1/me/source/slate", {"on_source_loss": False, "forced": False, "generation": slate["generation"]}, token=None, session=owner_token)
    run("docker", "pause", PUBLISHER)
    wait("disabled automatic slate reports no signal", lambda: api("GET", f"/v1/streams/{sid}/status")["status"] == "NO_SIGNAL")
    assert worker_id() == current, "disabled slate restarted worker"
    assert receiver_connections(destination_secret) == connections, "disabled slate reconnected RTMP output"
    slate = api("PUT", "/v1/me/source/slate", {"on_source_loss": True, "forced": False, "generation": slate["generation"]}, token=None, session=owner_token)
    wait("source stall activates fallback", lambda: api("GET", f"/v1/streams/{sid}/status")["status"] == "FALLBACK")
    time.sleep(12)  # Longer than the former disconnect grace.
    before = bytes_received()
    wait("fallback keeps sending media", lambda: bytes_received() > before + 10000)
    assert worker_id() == current
    assert receiver_connections(destination_secret) == connections, "fallback reconnected RTMP output"
    assert_slate(destination_secret)
    run("docker", "unpause", PUBLISHER)
    publish(stream)
    wait("source returns from fallback", live)
    assert worker_id() == current
    assert receiver_connections(destination_secret) == connections, "source return reconnected RTMP output"
    assert api("GET", f"/v1/streams/{sid}/status")["session"]["session_id"] == session_id
    assert_slate(destination_secret, expected=False)
    assert worker_id() == current
    assert receiver_connections(destination_secret) == connections
    wait("source remains live after decoded recovery", live)
    run("docker", "rm", "-f", PUBLISHER)
    wait("source disconnect activates fallback", lambda: api("GET", f"/v1/streams/{sid}/status")["status"] == "FALLBACK")
    wait("disconnected source permits fallback stop", lambda: api("GET", f"/v1/streams/{sid}/status")["can_stop"])
    api("POST", f"/v1/sessions/{session_id}/stop", {}, expected=202)
    wait("operator can stop fallback", lambda: not worker_id())
    wait("fallback stop reaches STOPPED", lambda: api("GET", f"/v1/streams/{sid}/status")["status"] == "STOPPED")
    print("Single-node acceptance passed", flush=True)


if __name__ == "__main__":
    try:
        main()
    finally:
        from pathlib import Path
        Path(".artifacts").mkdir(exist_ok=True)
        with open(".artifacts/control-diagnostics.log", "w") as log:
            subprocess.run(COMPOSE + ["logs", "--tail=60", "api", "agent", "edge", "receiver"], stdout=log, stderr=log)
            for container in run("docker", "ps", "-aq", "--filter", "label=streamtool.node=" + NODE).splitlines():
                subprocess.run(["docker", "logs", "--tail=80", container], stdout=log, stderr=log)
        subprocess.run(["docker", "rm", "-f", PUBLISHER], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        for stream_id in CREATED_STREAMS:
            # Stop only this run's sessions, after the edge has confirmed source
            # disconnection. Failed acceptance must not consume the next run's slot.
            deadline = time.monotonic() + 30
            while time.monotonic() < deadline:
                try:
                    session = api("GET", f"/v1/streams/{stream_id}/status")["session"]
                    if not session:
                        break
                    api("POST", f"/v1/sessions/{session['session_id']}/stop", {}, expected=202)
                    break
                except (AssertionError, OSError):
                    time.sleep(2)
        subprocess.run(["./tests/control/down.sh"], check=False)
