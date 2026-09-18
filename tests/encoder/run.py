"""Run only uniquely named disposable containers; retain JSON evidence."""
import argparse
import json
import pathlib
import subprocess
import tempfile
import time
import uuid

parser = argparse.ArgumentParser()
parser.add_argument("--width", type=int, default=1920)
parser.add_argument("--height", type=int, default=1080)
parser.add_argument("--fps", type=int, default=60)
parser.add_argument("--preset", choices=["veryfast","superfast","ultrafast"], default="veryfast")
parser.add_argument("--protocol", choices=["rtmp","srt"], default="rtmp")
parser.add_argument("--cpu", type=float, default=2)
parser.add_argument("--pattern", choices=["smpte","snow","ball"], default="smpte")
parser.add_argument("--source-preset", choices=["veryfast","superfast","ultrafast"], default="veryfast")
args = parser.parse_args()
tag = "streamtool-encoder-"+uuid.uuid4().hex[:10]
network = tag+"-net"
names = []
connections = []
root = pathlib.Path(__file__).resolve().parents[2]
evidence = root/".artifacts"/tag
evidence.mkdir(parents=True)

def run(*cmd):
    return subprocess.check_output(["docker",*map(str,cmd)],text=True).strip()

def container(role,image,command,options=()):
    name = tag+"-"+role
    names.append(name)
    run("run","-d","--name",name,"--network",network,"--label","streamtool.experiment="+tag,*options,image,*command)
    return name

def publisher():
    caps=f"video/x-raw,format=I420,width={args.width},height={args.height},framerate={args.fps}/1"
    sink = ["flvmux","name=m","streamable=true","!","rtmpsink","location=rtmp://media:1935/live/source"] if args.protocol=="rtmp" else ["mpegtsmux","name=m","!","srtsink","uri=srt://media:8890?streamid=publish:source&pkt_size=1316"]
    command=["-q",*sink,"videotestsrc","is-live=true","pattern="+args.pattern,"!",caps,"!","queue","!","x264enc","threads=2","tune=zerolatency","speed-preset=veryfast","bitrate=6000",f"key-int-max={args.fps*2}","!","h264parse","!","queue","!","m.","audiotestsrc","is-live=true","wave=sine","!","audio/x-raw,rate=48000,channels=2","!","audioconvert","!","avenc_aac","bitrate=160000","!","aacparse","!","queue","min-threshold-time=500000000","!","m."]
    command[command.index("speed-preset=veryfast")]="speed-preset="+args.source_preset
    return container("publisher-"+str(len(names)),"streamtool-control-publisher:latest",command)

try:
    run("network","create",network)
    with tempfile.TemporaryDirectory(prefix="streamtool-encoder-") as tmp:
        tmp=pathlib.Path(tmp)
        tmp.chmod(0o777)
        config=tmp/"mediamtx.yml"
        config.write_text("logLevel: info\nrtsp: false\nhls: false\nwebrtc: false\nmoq: false\nsrt: true\napi: true\nauthInternalUsers:\n  - user: any\n    permissions:\n      - action: publish\n      - action: read\n      - action: api\npaths:\n  all_others:\n")
        container("media","bluenviron/mediamtx:1.21.0",[],["--network-alias","media","-v",f"{config}:/mediamtx.yml:ro"])
        run("run","--rm","-v",f"{tmp}:/fixtures","streamtool-control-publisher:latest","-q","videotestsrc","pattern=ball","num-buffers=90","!","video/x-raw,format=I420,width=1280,height=720,framerate=30/1","!","x264enc","threads=2","tune=zerolatency","speed-preset=veryfast","!","h264parse","!","mp4mux","!","filesink","location=/fixtures/loop.mp4")
        pub=publisher()
        time.sleep(3)
        input_uri="rtmp://media:1935/live/source" if args.protocol=="rtmp" else "srt://media:8890?streamid=read:source"
        worker=container("worker","streamtool-encoder-experiment:local",["--width",str(args.width),"--height",str(args.height),"--fps",str(args.fps),"--preset",args.preset,"--input",input_uri,"--seconds","100"],["--cpus",str(args.cpu),"--memory","1g","-v",f"{tmp}:/fixtures:ro"])
        time.sleep(5)
        probe=container("probe","streamtool-encoder-experiment:local",["-u","/experiment/probe.py"],["--entrypoint","python3"])
        for phase,seconds in [("source",25),("fallback",25),("return",35)]:
            if phase=="fallback":
                (evidence/(pub+".log")).write_text(run("logs",pub))
                run("rm","-f",pub)
            elif phase=="return":
                pub=publisher()
            print("PHASE",phase,flush=True)
            time.sleep(seconds)
            print(run("logs","--tail","6",worker),flush=True)
            print(run("logs","--tail","5",probe),flush=True)
            state = json.loads(run("exec", worker, "python3", "-c", "import urllib.request;print(urllib.request.urlopen('http://media:9997/v3/paths/list').read().decode())"))
            output_path = next(p for p in state["items"] if p["name"]=="live/output")
            connections.append(output_path["source"])
            if not output_path["ready"] or connections[-1]!=connections[0]:
                raise RuntimeError("output connection changed")
        subprocess.check_output(["docker","wait",worker],text=True,timeout=30)
        time.sleep(1)
        for role,name in [("worker",worker),("probe",probe),("media",tag+"-media")]:
            (evidence/(role+".jsonl")).write_text(run("logs",name))
        (evidence/"settings.json").write_text(json.dumps(vars(args),indent=2))
        (evidence/"connections.json").write_text(json.dumps(connections,indent=2))
        code=run("inspect","-f","{{.State.ExitCode}}",worker)
        if code!="0":
            raise RuntimeError("encoder exited "+code)
        if run("inspect","-f","{{.State.ExitCode}}",probe)!="0":
            raise RuntimeError("decoder terminated")
        from verify import verify
        verify(evidence)
        print("EVIDENCE",evidence,flush=True)
finally:
    for name in names:
        result=subprocess.run(["docker","logs",name],capture_output=True,text=True)
        if result.returncode==0:
            (evidence/(name[len(tag)+1:]+".log")).write_text(result.stdout+result.stderr)
        subprocess.run(["docker","rm","-f",name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    subprocess.run(["docker","network","rm",network],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
