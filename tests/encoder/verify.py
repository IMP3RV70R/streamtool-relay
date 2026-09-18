import argparse
import json
from pathlib import Path

def verify(evidence):
    evidence=Path(evidence)
    args=argparse.Namespace(**json.loads((evidence/"settings.json").read_text()))
    connections=json.loads((evidence/"connections.json").read_text())
    assert len(connections)==3 and connections[0] and all(item==connections[0] for item in connections), connections
    worker_rows=[json.loads(line) for line in (evidence/"worker.jsonl").read_text().splitlines() if line.startswith("{")]
    decoded=[json.loads(line) for line in (evidence/"probe.jsonl").read_text().splitlines() if line.startswith("{") and json.loads(line)["event"]=="decoded"]
    summary=next(row for row in worker_rows if row["event"]=="complete")
    assert summary["backwards_pts"]==0 and summary["max_gap_ms"]<=100, summary
    assert summary["loops"]>=20, summary
    assert summary["frames"]>=args.fps*100*.95 and summary["media_seconds"]>=95, summary
    metrics={}
    for phase,low,high,mode in [("source",10,25,"source"),("fallback",40,50,"fallback"),("return",70,90,"source")]:
        samples=[row for row in worker_rows if row["event"]=="sample" and low<=row["elapsed"]<=high]
        assert samples, phase
        for row in samples:
            assert row["mode"]==mode and row["fps"]>=args.fps*.95, (phase,row)
            assert 5.4<=row["video_mbps"]<=6.6, (phase,row)
            if mode=="source":
                assert row["input_fps"]>=args.fps*.95, (phase,row)
        metrics[phase]={"mean_cpu_cores":round(sum(row["cpu_cores"] for row in samples)/len(samples),3),"max_cpu_cores":max(row["cpu_cores"] for row in samples),"max_cgroup_memory_mib":max(row["cgroup_memory_mib"] for row in samples),"min_encoded_fps":min(row["fps"] for row in samples)}
    for phase,low,high,slate in [("source",10,20,False),("fallback",40,45,True),("return",65,75,False)]:
        samples=[row for row in decoded if low<=row["elapsed"]<=high]
        assert samples, (phase,"no decoded samples")
        for row in samples:
            assert row["fps"]>=args.fps*.95 and row["backwards_pts"]==0, (phase,row)
            assert f"width=(int){args.width}" in row["profile"] and f"height=(int){args.height}" in row["profile"] and f"framerate=(fraction){args.fps}/1" in row["profile"], row
            assert (row["brightness"]<20)==slate, (phase,row)
            assert (row["audio_peak"]<=64)==slate, (phase,row)
            if slate:
                assert row["motion_peak"]>20, (phase,row)
    print("PASS decoded source → moving silent fallback → source, same RTMP connection",flush=True)
    (evidence/"result.json").write_text(json.dumps({"settings":vars(args),"metrics":metrics,"output":summary},indent=2))
    print(json.dumps(metrics),flush=True)

if __name__=="__main__":
    import sys
    verify(sys.argv[1])
