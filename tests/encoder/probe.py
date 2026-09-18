import json
import struct
import time
import gi
gi.require_version("Gst", "1.0")
from gi.repository import Gst
Gst.init(None)
p = Gst.parse_launch("""rtmpsrc location=rtmp://media:1935/live/output ! flvdemux name=d
 d.video ! queue ! h264parse ! avdec_h264 max-threads=2 ! identity name=profile ! videoconvert ! videoscale ! video/x-raw,format=RGB,width=160,height=90 ! appsink name=v sync=false max-buffers=2 drop=true
 d.audio ! queue ! aacparse ! avdec_aac ! audioconvert ! audio/x-raw,format=S16LE ! appsink name=a sync=false max-buffers=8 drop=true
""")
p.set_state(Gst.State.PLAYING)
v, a = p.get_by_name("v"), p.get_by_name("a")
started = sampled = time.monotonic()
frames = peak = motion = 0
brightness = []
previous = None
last_pts = None
max_gap = backwards = 0
try:
    while time.monotonic()-started < 110:
        msg = p.get_bus().pop_filtered(Gst.MessageType.ERROR | Gst.MessageType.EOS)
        if msg:
            if msg.type == Gst.MessageType.EOS and time.monotonic()-started>=90:
                print(json.dumps({"event":"complete", "backwards_pts":backwards,"max_gap_ms":round(max_gap/1e6,2)}),flush=True)
                break
            raise RuntimeError(str(msg.parse_error()) if msg.type == Gst.MessageType.ERROR else "EOS")
        s = v.emit("try-pull-sample", 10000000)
        if s:
            b = s.get_buffer()
            data = b.extract_dup(0, b.get_size())
            brightness.append(sum(data)/len(data))
            if previous:
                motion = max(motion, max(abs(x-y) for x,y in zip(data[::12],previous[::12])))
            previous = data
            frames += 1
            if last_pts is not None:
                max_gap = max(max_gap, b.pts-last_pts)
                backwards += int(b.pts <= last_pts)
            last_pts = b.pts
        while True:
            s = a.emit("try-pull-sample", 0)
            if not s:
                break
            b = s.get_buffer()
            data = b.extract_dup(0,b.get_size())
            peak = max(peak,max(abs(x[0]) for x in struct.iter_unpack("<h",data)))
        now = time.monotonic()
        if now-sampled >= 5:
            caps = p.get_by_name("profile").get_static_pad("src").get_current_caps()
            print(json.dumps({"event":"decoded", "elapsed":round(now-started,2),"fps":round(frames/(now-sampled),2),"brightness":round(sum(brightness)/len(brightness),2) if brightness else None,"motion_peak":motion,"audio_peak":peak,"max_gap_ms":round(max_gap/1e6,2),"backwards_pts":backwards,"profile":caps.to_string() if caps else None}),flush=True)
            sampled,frames,peak,motion,brightness = now,0,0,0,[]
finally:
    p.set_state(Gst.State.NULL)
