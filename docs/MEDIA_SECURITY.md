# Media runtime security

## Current runtime

The distributed worker builds only the required GStreamer elements/plugins and
native ELF dependency closure. Upstream sources are pinned by version and SHA256
in `backend/scripts/media-sources.json`; configuration, licenses and source SBOM
are retained. Debian package inventory is preserved for remaining OS libraries.
The broader fixture publisher is not the distributed worker.

H.264 uses OpenH264 decoding and one x264 encoder; AAC input uses FAAD and output
uses voaacenc. Ordinary 8-bit 4:2:0 Baseline/Main/High profiles are supported.
MP4 validation requires matching AVC/SPS metadata and rejects ancillary track
handlers and unsupported High 10/4:2:2/4:4:4 profiles before storage. Uploaded
PNG/JPEG is bounded and canonicalized by Go. Metadata validation does not prove
that every compressed frame is well formed; native media remains untrusted work.

Private Agent handoff uses bounded Go `runtime-io` file/archive operations, staged
atomic resource replacement and activation after validation. GNU archive/file tools,
BusyBox and libacl are absent; the managed dash/sleep startup barrier remains.
Failure stops the new worker. Secrets are private tmpfs files, not environment values.

## Scan policy

Every candidate must scan its exact exported API/worker/proxy/edge image IDs with
Trivy and separately scan upstream components with Grype. Validate expected OS/source
coverage and deliberately vulnerable source controls against the same valid database.
HIGH/CRITICAL findings, unavailable/stale databases, missing reports or incomplete
coverage fail closed. No severity waivers, ignore-unfixed or blanket VEX exemptions.

Scanners receive no host secrets, Docker socket, capabilities or root access.
Database download/hydration uses temporary cache on the report filesystem;
allow space for databases, temporary files and native build layers. Reports under
`.artifacts` are local outputs and are not distributed with source.

```sh
make security-images
bash infra/release/scan.sh .artifacts/streamtool-relay-VERSION-ARCHITECTURE
```

The second command requires the actual extracted candidate directory and its images
loaded locally. See [release procedure](RELEASE_BUILD.md).

## Latest local evidence

The complete patched native ARM64 candidate from 2026-09-17 passed exact Trivy image
and Grype upstream gates with **0 HIGH / 0 CRITICAL** and detected both intentionally
vulnerable source controls. Eleven Medium package findings remain for review.
This is point-in-time evidence for those exact images, not absence of vulnerabilities
or acceptance of every later build. Current bundles need rebuilding for the latest
installer contract. The GitHub Linux amd64 image gate also passed; a complete current native release
bundle and production provenance/signing are not accepted.

The same worker passed eight-output 720p30 fallback/recovery and separate 1080p60
moving-MP4 loss/return/stop checks. Short local ARM64 CPU/RAM samples do not establish
VDS capacity. The results apply to that local candidate; they do not accept a later bundle.

## Isolation and limits

Workers use UID 65532, read-only root, dropped capabilities, no-new-privileges,
bounded tmpfs/CPU/RAM/PIDs and namespace firewall/traffic policing. Only the trusted
Agent delivers scoped secrets. Docker access makes the Agent host-administrative;
it remains outside the worker trust boundary.

Local namespace checks verified private/metadata denial, the exact source exception
and traffic limits. Target-host install/policy/resource checks, real Twitch and the
24-hour run remain separate acceptance work. A container boundary and clean scan
do not replace an independent audit or patch review.

See [network policy](runbooks/security-and-network.md),
[cabinet security](CABINET_SECURITY.md), [current status](STATUS.md) and
[publication gates](PRODUCTION.md).
