"""Isolated feasibility experiment, never used by the production worker."""
import argparse
import json
import resource
import socket
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit
import time

import gi
gi.require_version("Gst", "1.0")
from gi.repository import Gst

Gst.init(None)
parser = argparse.ArgumentParser()
parser.add_argument("--width", type=int, default=1920)
parser.add_argument("--height", type=int, default=1080)
parser.add_argument("--fps", type=int, default=60)
parser.add_argument("--preset", default="veryfast")
parser.add_argument("--input", default="rtmp://media:1935/live/source")
parser.add_argument("--output", default="rtmp://media:1935/live/output")
parser.add_argument("--seconds", type=int, default=100)
args = parser.parse_args()

# Resolve the local test host before constructing native SRT pipelines. The
# current GLib async DNS cancellation path can abort the whole process on retry.
def resolved_uri(uri):
    parts = urlsplit(uri)
    ip = socket.gethostbyname(parts.hostname)
    return urlunsplit((parts.scheme, ip+(":"+str(parts.port) if parts.port else ""), parts.path, parts.query, parts.fragment))

args.input, args.output = resolved_uri(args.input), resolved_uri(args.output)
caps = f"video/x-raw,format=I420,width={args.width},height={args.height},framerate={args.fps}/1"
queue = "queue max-size-time=300000000 max-size-bytes=0 max-size-buffers=0 leaky=downstream"

fallback = Gst.parse_launch(f"filesrc location=/fixtures/loop.mp4 ! qtdemux ! h264parse ! avdec_h264 max-threads=2 ! videoconvert ! videoscale ! videorate ! {caps} ! intervideosink channel=fallback sync=true")
output = Gst.parse_launch(f"""
 input-selector name=v sync-streams=true sync-mode=clock cache-buffers=true drop-backwards=true ! {queue} !
 x264enc threads=2 tune=zerolatency speed-preset={args.preset} bitrate=6000 key-int-max={args.fps*2} pass=cbr nal-hrd=cbr option-string=force-cfr=1 !
 h264parse config-interval=-1 ! video/x-h264,stream-format=avc,alignment=au ! identity name=encoded ! queue ! mux.
 flvmux name=mux streamable=true ! rtmpsink location={args.output} sync=true
 intervideosrc channel=fallback timeout=10000000000 ! {caps} ! {queue} ! v.sink_0
 intervideosrc channel=source timeout=3000000000 ! {caps} ! {queue} ! v.sink_1
 input-selector name=a sync-streams=true sync-mode=clock cache-buffers=true drop-backwards=true ! audioconvert ! avenc_aac bitrate=160000 ! aacparse ! queue ! mux.
 audiotestsrc is-live=true wave=silence ! audio/x-raw,rate=48000,channels=2 ! {queue} ! a.sink_0
 interaudiosrc channel=source ! audio/x-raw,rate=48000,channels=2 ! {queue} ! a.sink_1
""")
selectors = [output.get_by_name(n) for n in ("v", "a")]
for selector in selectors:
    selector.set_property("active-pad", selector.get_static_pad("sink_0"))
last_frame = 0.0
frames = 0
input_frames = encoded_bytes = 0
first_pts = last_pts = None
max_gap = 0
backwards = 0

def source_frame(pad, info):
    global last_frame, input_frames
    last_frame = time.monotonic()
    input_frames += 1
    return Gst.PadProbeReturn.OK

def encoded_frame(pad, info):
    global frames, first_pts, last_pts, max_gap, backwards, encoded_bytes
    b = info.get_buffer()
    if b is not None and b.pts != Gst.CLOCK_TIME_NONE:
        frames += 1
        encoded_bytes += b.get_size()
        if first_pts is None:
            first_pts = b.pts
        if last_pts is not None:
            backwards += int(b.pts <= last_pts)
            max_gap = max(max_gap, b.pts - last_pts)
        last_pts = b.pts
    return Gst.PadProbeReturn.OK

output.get_by_name("encoded").get_static_pad("src").add_probe(Gst.PadProbeType.BUFFER, encoded_frame)

def new_source():
    if args.input.startswith("srt:"):
        head = f"srtsrc uri={args.input} ! tsdemux name=d"
    else:
        head = f"rtmpsrc location={args.input} ! flvdemux name=d"
    video_pad, audio_pad = (" ! video/x-h264", " ! audio/mpeg,mpegversion=4") if args.input.startswith("srt:") else ("video", "audio")
    p = Gst.parse_launch(f"""{head}
 d.{video_pad} ! queue ! h264parse ! avdec_h264 max-threads=2 ! videoconvert ! videoscale ! videorate ! {caps} ! identity name=fresh ! intervideosink channel=source sync=false async=false
 d.{audio_pad} ! queue ! aacparse ! avdec_aac ! audioconvert ! audioresample ! audio/x-raw,rate=48000,channels=2 ! interaudiosink channel=source sync=false async=false
 """)
    p.get_by_name("fresh").get_static_pad("src").add_probe(Gst.PadProbeType.BUFFER, source_frame)
    p.set_state(Gst.State.PLAYING)
    return p

fallback.set_state(Gst.State.PLAYING)
output.set_state(Gst.State.PLAYING)
source = None
next_source = 0.0
mode = "fallback"
started = sampled = time.monotonic()
usage = resource.getrusage(resource.RUSAGE_SELF)
cpu = usage.ru_utime + usage.ru_stime
sample_frames = 0
sample_input = sample_bytes = 0
loops = 0
try:
    while time.monotonic() - started < args.seconds:
        now = time.monotonic()
        msg = output.get_bus().pop_filtered(Gst.MessageType.ERROR | Gst.MessageType.EOS)
        if msg:
            raise RuntimeError(str(msg.parse_error()) if msg.type == Gst.MessageType.ERROR else "output EOS")
        msg = fallback.get_bus().pop_filtered(Gst.MessageType.ERROR | Gst.MessageType.EOS)
        if msg:
            if msg.type == Gst.MessageType.ERROR:
                raise RuntimeError(str(msg.parse_error()))
            if not fallback.seek_simple(Gst.Format.TIME, Gst.SeekFlags.FLUSH | Gst.SeekFlags.KEY_UNIT, 0):
                raise RuntimeError("fallback seek failed")
            loops += 1
        if source:
            msg = source.get_bus().pop_filtered(Gst.MessageType.ERROR | Gst.MessageType.EOS)
            if msg or now - last_frame > 5 and now - next_source > 5:
                if msg:
                    print(json.dumps({"event":"input_restart", "elapsed":round(now-started,2), "reason":str(msg.parse_error()) if msg.type == Gst.MessageType.ERROR else "EOS"}))
                source.set_state(Gst.State.NULL)
                source = None
                next_source = now + 1
        elif now >= next_source:
            source = new_source()
            next_source = now
        desired = "source" if now - last_frame < 1 else "fallback"
        if desired != mode:
            mode = desired
            for selector in selectors:
                selector.set_property("active-pad", selector.get_static_pad("sink_1" if mode == "source" else "sink_0"))
            print(json.dumps({"event": "switch", "mode": mode, "elapsed": round(now-started, 2)}))
        if now - sampled >= 5:
            u = resource.getrusage(resource.RUSAGE_SELF)
            current = u.ru_utime + u.ru_stime
            memory = int(Path("/sys/fs/cgroup/memory.current").read_text())/(1<<20)
            print(json.dumps({"event": "sample", "mode": mode, "elapsed": round(now-started, 2), "cpu_cores": round((current-cpu)/(now-sampled), 3), "fps": round((frames-sample_frames)/(now-sampled), 2), "input_fps":round((input_frames-sample_input)/(now-sampled),2), "video_mbps":round((encoded_bytes-sample_bytes)*8/1e6/(now-sampled),2), "peak_memory_mib": round(u.ru_maxrss/1024, 1), "cgroup_memory_mib": round(memory, 1), "loops": loops}))
            sampled, cpu, sample_frames = now, current, frames
            sample_input, sample_bytes = input_frames, encoded_bytes
        time.sleep(0.02)
    print(json.dumps({"event": "complete", "frames": frames, "backwards_pts": backwards, "max_gap_ms": round(max_gap/1e6, 2), "media_seconds": (last_pts-first_pts)/1e9 if first_pts is not None else 0, "loops": loops}))
finally:
    for p in (source, fallback, output):
        if p:
            p.set_state(Gst.State.NULL)
