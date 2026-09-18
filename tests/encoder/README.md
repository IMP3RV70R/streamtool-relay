# Common encoder feasibility test

This is an isolated feasibility experiment. The current production worker uses
the common continuous encoder; these containers do not modify deployed workers.

Requirements: Docker, Python 3, and the existing fixture publisher image. From the
repository root:

```sh
docker build --target fixture-publisher -t streamtool-control-publisher:latest backend
docker build -t streamtool-encoder-experiment:local tests/encoder
python3 tests/encoder/run.py --protocol srt
python3 tests/encoder/run.py --protocol rtmp --pattern snow --source-preset ultrafast
```

Run benchmarks sequentially. Publisher and decoder run in separate containers;
their CPU/memory are excluded from encoder measurements. They still compete for
the host's physical resources. The encoder is restricted to 2 CPU cores and 1 GiB
by default. Use `--cpu`, `--width`, `--height`, `--fps`, and `--preset` to compare
configurations. Native ARM measurements must not be treated as x86 VDS capacity.

The test generates a three-second H.264 MP4 at 720p30 and plays it repeatedly in
an independent decoding pipeline. It never pre-encodes fallback to output profiles
or caches compressed output packets. Raw `intervideo`/`interaudio` channels feed
synchronized `input-selector` elements before one persistent H.264/AAC encoder
and RTMP connection. Input loss switches video and audio to the moving clip and
silence; returning input is decoded and selected again. Output is 1080p60,
6000 kbps H.264, 160 kbps AAC unless overridden.

A separate RTMP consumer decodes the actual transmitted video and audio. Assertions
check resolution/FPS, source/fallback identity, motion over repeated clip loops,
silence during fallback, monotonic timestamps and output connection identity
across source loss/return. A normal EOS is allowed only at the end of the experiment.
Evidence is retained in `.artifacts/streamtool-encoder-<unique id>/`. Containers and
networks are unique and removed without touching other stacks or database volumes.
Saved evidence can be checked again without Docker:

```sh
python3 tests/encoder/verify.py .artifacts/streamtool-encoder-<unique-id>
```

Stable-phase checks require incoming/encoded/decoded frame rates within 5% of the
target, encoded video bitrate within 10% of 6 Mbps, and at least 95 seconds of
monotonic output over the 100-second test. `result.json` contains phase CPU/memory
and encoded FPS. Cgroup memory is current container memory; `ru_maxrss` is a
process lifetime high watermark and must not be presented as current consumption.

SMPTE is an easy mostly static input. Full-frame changing noise (`snow`) provides
an intentionally difficult encoding stress case; it is not representative IRL
footage or a perceptual-quality test. An ultrafast *publisher* keeps input generation
from becoming the bottleneck; the common output encoder still uses `veryfast`.

This test does not establish Twitch acceptance, a 24-hour soak, security acceptance,
full application RAM, Selectel performance, or backup/update readiness. Hardware
encoding is not used. Target-VDS capacity is not established by this experiment.
