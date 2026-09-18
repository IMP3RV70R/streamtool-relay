# streamtool-relay architecture

The pre-launch self-hosted product runs on one VDS for one owner, one SRT/RTMP
source and up to eight independent RTMP/RTMPS outputs. PostgreSQL, hourly billing, public registration,
multi-node placement and cloud fencing are outside this runtime.

## Processes and authority

```text
source --SRT/RTMP--> MediaMTX --private SRT--> per-session worker --RTMP/S--> Twitch
                        |                         |
                   authenticated             source/fallback
                   observations             raw selectors
                        |                         |
                    API/controller             ONE encoder
                        |
                 private SQLite WAL
                        |
                 mTLS host Node Agent --> isolated Docker worker

browser --HTTPS--> Caddy --verified private TLS--> API
```

SQLite is the durable authority for owner/session authentication, routing, hashes,
encrypted output secrets, asset generations, publisher observations, session intent,
allocation identity and controller fencing. One process holds an exclusive file lock;
IMMEDIATE transactions serialize admission and state changes. WAL/FULL synchronization
and checksummed migrations protect durability. There are no database network credentials.

The Agent has a durable fence high watermark. Its authenticated heartbeat exposes
that watermark so a restored controller database can advance safely. Deadlines,
generations and fencing protect commands; healthy API/Agent restarts adopt an existing
worker. A worker has private session files, CPU/memory caps and a namespace network
policy installed before activation. Docker access makes the Agent part of the trusted host.

## Media contract

The edge admits one publisher per source and serves only an allocation-scoped signed
read token to a running session. Video H.264/AAC arrives through MPEG-TS/private SRT.
A separate source decoder reconnects without replacing the output pipeline. Parsed
video metadata is checked before decoder allocation for the supported 1080p60 bounds.
Supported input dimensions/FPS need not match the output; scale/rate conversion normalizes
input to the chosen output quality. The default is 720p30/3000 kbps.

Raw video/audio selectors feed one continuous x264/AAC encoder. The output clock,
CBR, keyframe cadence and codec headers are owned by this pipeline across source and
fallback transitions. PNG/JPEG becomes a frozen image; H.264 MP4 is decoded and looped
directly, with silence. There is no per-profile encoded preparation/cache. Separate
bounded queues and a guarded mux/sink for each destination allow output retries without replacing source
observation, fallback or the encoder. Source DNS is resolved with bounded Go calls
before native SRT attempts, avoiding GLib cancellation failures.

When automatic fallback is disabled and the source disappears, NO_SIGNAL sends
continuous black video and silence. It does not send GAP packets through RTMP;
those can corrupt framing on resumption. Missing frames never authorize stopping:
explicit completion requires confirmed publisher disconnection and a fresh successful
edge snapshot. Forced fallback preserves whether the publisher is still live.

## Failure behavior

API/SQLite shutdown pauses control/new sessions; an existing worker continues media.
Source loss switches fallback automatically; source return keeps the session/output.
All destinations share the same encoded video/audio. Egress reservations and namespace
policing scale with enabled destination count; CPU remains reserved for one encoder.
An output outage retries only its branch with a bounded circuit; explicit retry advances
the output generation. Worker crash creates a bounded replacement and a new output
connection. Whole-VDS/edge failure interrupts the stream and does not promise seamless
server failover. User stop is durable and cannot resurrect without a new publisher.

Backup includes SQLite, encryption keys, private certificates/configuration and Agent
fencing. Restore requires maintenance, preserves history and marks old active sessions
ended so an older backup cannot resume a broadcast completed after its snapshot.

See [product agreement](PRODUCT_ALIGNMENT.md), [self-host operations](SELFHOST.md)
and [API contract](../contracts/user-api.md). Real Twitch/target-host/24-hour acceptance
and Linux policy/maintenance rehearsal remain separate from local tests.
