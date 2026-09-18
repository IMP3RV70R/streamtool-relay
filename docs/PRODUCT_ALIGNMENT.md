# Product contract

Updated: 2026-09-18. This file is authoritative for product scope and lifecycle.
Implementation and verification limits are recorded in [STATUS.md](STATUS.md).

## Lifecycle

**PRE-LAUNCH.** No public production release has been published. Move unreleased
clients, APIs and internal models directly to the intended contract; do not add
aliases, deprecated paths or compatibility shims solely for pre-release behavior.
Preserve stored data, ordered migrations, security/recovery guarantees and user work.

The first accepted production publication must change this status to **PUBLISHED**
and establish a compatibility/versioning policy in the same release. Publishing
source alone does not establish production acceptance.

## Scope

**streamtool-relay** is a self-hosted application: one owner, one VDS, one SRT/RTMP
source, source-loss protection and up to eight independent RTMP/RTMPS outputs.
Twitch is the primary destination; users provide the ingest URL and stream key.
The website, backend and existing native Android app are active product targets.
The product term is **source** («источник»), which may be any application or device.

No hourly billing, public registration, email dependency, Twitch OAuth, platform
metadata, analytics, chat, moderation or Telegram runtime. Optional hosting referrals
must not restrict hosting choice or require a cloud account.

Project code uses [MIT](../LICENSE). Third-party software retains its own licenses
and distribution obligations. GitHub Actions/Releases are the selected distribution
provider. The repository is [IMP3RV70R/streamtool-relay](https://github.com/IMP3RV70R/streamtool-relay).
A signed installation preview uses the separately pinned `preview` channel.
No stable production release or public update feed is configured.

## Stream behavior

Routing is always available. Clients provision the source automatically after owner
authentication; there is no global routing switch. Individual outputs can still be
disabled. Provisioning alone does not start an encoder or broadcast.

One continuous H.264/AAC encoder receives decoded source or fallback; all outputs
share its quality. Separate bounded queues/muxers/sinks isolate destination failure,
retry and edits from peers and the encoder. Each session has its own isolated worker.
All enabled outputs count toward bandwidth admission/policing with headroom.
The quota is eight configured outputs, including disabled ones; archived outputs
free a slot. Quota and generation checks must remain atomic.

Standard output supports even dimensions up to 1920×1080 (or portrait equivalent),
15–60 FPS including fractional rates, video 100–8000 kbps and audio 64–320 kbps,
CBR and a two-second GOP. Defaults are 720p30/3000 kbps. Input size/FPS need not
match the output. Higher configured bitrates do not promise destination acceptance;
Enhanced Broadcasting, HEVC, multiple tracks and 1440p are deferred.

Fallback is PNG/JPEG or one 8-bit 4:2:0 H.264 Baseline/Main/High MP4, at most
30 seconds, 50 MiB and 1080p. Uploaded audio is ignored. Clips are decoded/looped
with silence, without per-profile preparation or encoded caches. SQLite owns asset
content/generation. Quality and asset changes require no session and a disconnected
publisher; users need not choose a profile before first use.

Source loss or disconnect does not end a broadcast. Automatic fallback keeps the
output connection and returns to the source when it recovers. Forced fallback may
be selected while the source remains live. If automatic fallback is disabled,
NO_SIGNAL sends black video and silence. Explicit completion requires a confirmed
publisher disconnect and a fresh successful edge observation; missing/stale/unknown
frames cannot unlock stop. A future publisher connection starts a new session.
Whole-VDS failure interrupts media; seamless cross-host failover is not promised.

## Authority and authentication

SQLite is the durable runtime authority: WAL/FULL synchronization, exclusive control
process, IMMEDIATE transactions and checksummed ordered migrations. Preserve
controller/Agent monotonic fencing, bounded recovery and control/media separation.
Historical PostgreSQL migrations/volumes are not erased or automatically converted.
Explicit pre-launch resets in migrations 000022/000027 remain the agreed exceptions.

First owner setup requires the unique installation token, chosen password and newly
enrolled TOTP confirmation before a session exists. Provide local QR/manual enrollment
and ten hash-only single-use recovery codes. Login requires password plus TOTP or a
recovery code. Factor consumption is atomic/durable; TOTP secrets are encrypted.
Authenticator replacement requires current password and a valid second factor,
then new TOTP confirmation, revoking old sessions/recovery codes.

Preserve owner/password/media/key data during recovery. Offline SSH owner recovery
revokes authentication and requires fresh TOTP enrollment; it does not rotate media
or encryption keys or introduce password-only HTTP login. Setup remains a singleton.
Source keys are hash-only; output keys are encrypted/write-only. Secrets must not
appear in logs, URLs, read responses or process arguments.

## Installation and updates

The Android onboarding protocol accepts public IP/root SSH, installs the application
and transfers owner setup to the native cabinet. Its host allowlist is clean Debian 13
or Ubuntu 24.04. Default builds disable deployment without distribution configuration.

The welcome screen has “Подключиться” and “Настроить”. SSH uses one address field
with default port 22 or an explicit port (`IP:port`, `[IPv6]:port`). No separate
connection-test step. Confirm first-use host identity before sending credentials;
pin it to IP/port. A changed key requires explicit confirmation of the old/new
fingerprints before replacing the pin and sending credentials; it does not permanently
block installation after a server reinstall. SSH credentials stay on the client in
memory; the cabinet password is independent. There is no browser SSH relay.

Use a fixed signed distribution path and resumable bounded root host job, not
unsigned remote scripts. Verify independent pinned trust before executing package
code. Preserve other workloads, SSH access and committed owner/configuration/keys.
A disconnect must resume the same job. First installation has no previous version
for rollback. Transfer setup authority over pinned SSH in memory to the same-IP
normally verified HTTPS cabinet; never expose it in a URL or terminal-copy step.
Public IP certificate issuance/renewal/reload is unverified on a real host.

The restricted owner update API is disabled by default; graphical update controls
and public discovery are absent. Authorization requires a fresh TOTP in the signed-in
session, without repeating the password or accepting a recovery code. Factor
consumption is bound atomically to the exact release/request with bounded attempts
and idempotent retry. Installation requires no active broadcast and a maintenance gate.

Before replacement, retain verified SQLite and the complete matching previous
installation/images/helpers. An independent coordinator must survive API/Agent/host
restart, restore the matching version and database on installation/health failure,
verify readiness and resume rollback idempotently with bounded attempts. Never run
old binaries against a newer schema. Failed bounded recovery stays fenced and
requires administrative recovery; do not promise success after host/device failure.

## Verification limits

Local storage/API/media, Android and isolated Linux recovery checks are recorded in
[current status](STATUS.md). Unit tests, mocked APIs, ARM measurements and controlled
certificates do not establish real VDS, public ACME or Twitch acceptance. There is
no accepted production release or target-host 24-hour run.
