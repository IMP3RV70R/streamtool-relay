# Independent host update coordinator

Implemented 2026-09-17: root-only offline signed queue, durable phase journal,
application admission fence, fixed Linux Docker/systemd operations, matching
installation/SQLite recovery and bounded crash resumption. This is implemented and
partly locally verified, not production-accepted. The restricted Unix protocol,
owner TOTP API and durable dispatch are implemented but disabled by default.
Network discovery and graphical controls are not implemented. Public update controls require the remaining distribution/acceptance gates.
Initial Android installation uses its [separate protocol](ONBOARDING.md).

## Runtime and privilege

Initial installation installs `streamtool-updater.service` and its Python/verifier
runtime into `/usr/local/libexec/streamtool-updater`. The oneshot service resumes the
current job at boot or explicit start independently of API/Agent containers. The
application update never replaces/stops this runtime or its systemd unit. Protocol
changes need separately recoverable updater distribution.

`/var/lib/streamtool-updater/journal` is root-private (0700). Each canonical UUID job
has its own private staging, recovery set and journal. The current-job pointer and
phase transitions use fsync + atomic rename. A coordinator OS lock serializes job
queue/execution. A separate root-owned `streamtool-updater-bridge.service` exposes
only a fixed local socket; neither journal, shell nor Docker socket is mounted into
the API. Administrative CLI paths are permitted only to Linux root and are not part
of the owner protocol. Application replacement never replaces/stops either host
service, its runtime or its journals.

Trust uses the independently pinned `/etc/streamtool/release-trust.json`; a signing
seed is never installed. Queue verifies exact signed metadata and archive with the
existing trusted `release-tool`, not target code. Before maintenance mutation it
revalidates signature/expiry/watermark, host architecture and staged checksums.
`target_schema` in the signed manifest identifies the exact expected running schema.
No public feed or production signing key has been established. See [release trust](RELEASE_CHANNEL.md).

## Restricted bridge and durable delivery

`/var/lib/streamtool-updater/socket` belongs to root:65532 with mode 0750; its
`control.sock` is root:65532 mode 0660. The API mounts the directory read-only at
`/updater`. The Python server checks Linux SO_PEERCRED for UID 65532; the Go client
requires a root peer. This fixed deployment requires ordinary Docker UID mapping;
user-namespace remapping is not silently supported. UID 65532 must not be shared
with other untrusted host workloads. API/client secrets never cross this socket.

Protocol 1 uses one newline-terminated JSON request/response per connection.
Commands are `catalog`, `status` (optional canonical UUID) and `start` (UUID + exact
manifest digest). Unknown/duplicate fields, wrong protocol/types, arbitrary paths,
URLs and commands are rejected. Server bodies are limited to 4 KiB/32 KiB, four
concurrent connections and bounded reads. Client has an eight-second deadline,
context cancellation and bounded strict response decoding.

Root may prepare one offline candidate with `updater_bridge.py offer` using fixed
trusted verification. The private catalog retains manifest, signature and bounded
archive; complete verification/private staging precedes publication of its pointer.
Every catalog/start rechecks the signed metadata's trust, expiry, architecture,
compatibility and anti-rollback watermark. No update-feed discovery/download exists yet; initial Android download is separate.

Owner `GET /v1/me/updates` reads installed build version, candidate, actual host
state and pending local request. `POST /v1/me/updates` accepts only `request_id`,
`release_digest` and `code`. Same-origin/cookie checks, existing persistent auth
budgets and fresh six-second idle observation precede new TOTP authorization.
Password/recovery fields and ambiguous/trailing JSON are rejected. HTTP 202 means
a durable application receipt; it does not mean installation started. Exact retries
remain receipts even after the candidate changes; cancelled/expired pending receipts
cannot be revived. Root performs its final idle check independently under the gate.

`UPDATE_OWNER_ENABLED=false` is the bootstrap default. Enabling owner routes also
requires `UPDATE_SOCKET` and maintenance configuration. No cabinet button enables
this switch. The dispatcher starts with the API and checks its durable outbox every
five seconds. Under the shared maintenance lock and SQLite transaction it checks
the original session/owner and expiry, then transfers the same UUID/digest to root.
Uncertain delivery leaves the request pending; target/conflict refusal cancels it.
Positive matching root receipts mark it dispatched. No factor or plaintext session
is retained for retries. Replacement/recovery/snapshot restoration cancels pending
authorization atomically with credential revocation.

Root writes its receipt before publishing `requested.json` or calling systemd.
An exact retry repairs interrupted pointer publication, never creates another job;
orphan active receipts refuse competing requests. Root requests expire after 24
hours and systemd wake attempts are durable and bounded to eight. A wake failure
returns unavailable so the API keeps delivering; exhaustion without a host job
records FAILED before application mutation. If a host job already exists, leave
its journal/gate untouched and stop additional wakes; only the engine or emergency
administrator can resolve it. Service resume/boot verifies and queues the root request,
then runs the existing independent engine. A queue failure without a durable job
is terminal FAILED; an existing uncertain job is preserved for engine resumption.
The inbox is capped at 4096 receipts; cleanup/retention tooling remains unfinished.
Expired QUEUED engine jobs are refused before preparation, so installation cannot
begin after its recovery budget has already expired.

## Admission and final idle

The API has a read-only mount of `/var/lib/streamtool-updater/admission` at
`/maintenance`; Agent uses the host path. Both are configured explicitly by the
self-host bootstrap. Admission uses shared flock on the host-owned 0644 lock and
rejects persistent `active.json`, missing lock or any other gate error. Hold the
shared lock through every API mutation, publisher authorization, controller cycle
and Agent start. No login/TOTP/recovery-code/configuration mutation can slip into
the snapshot interval. GET/readiness remains available.

The coordinator waits at most 55 seconds for admitted work, holds exclusive flock, writes a durable owner marker, then checks
actual mount/running gate configuration, version/schema, Docker owned workers,
active SQLite sessions/connections/allocations and fresh edge observation. Unknown
publisher state or an active broadcast causes refusal without stopping it. The
exclusive lock serializes against already admitted work. After process death the
marker continues to fence starts/mutations; only verified commit/recovery removes it.
Manual backup/restore/private leaf renewal use the same maintenance lock. Renewal
also checks active source/session and fresh observations before restarting services.

Development control fixtures without the host updater may omit maintenance config.
Such an installation cannot pass updater preflight. Old packages without coordinator
artifacts are rejected by host preflight; no pre-launch unsigned-update alias remains.

## Transaction and recovery

A job progresses through QUEUED → PREPARING → INSTALLING → VERIFYING → COMMITTED →
SUCCEEDED. Preflight validates new Compose service ownership/configuration and reserves
conservative working space plus a physically allocated emergency reserve on each
participating filesystem. Preparation stops the idle API, takes the trusted SQLite
backup, saves immutable old OCI images offline and captures old UI/configuration,
Agent/policy/sudoers, services and maintenance helpers. Fsync and verify the complete
recovery inventory before marking INSTALLING. Preserve the independent fence watermark.

Installation loads verified images, stops only installation-owned services, copies
coherent target files, restarts and lets the API apply ordered/checksummed migrations.
Teardown selects the stable `streamtool-selfhost` ownership label rather than relying
on a possibly broken target Compose file. Unexpected session workers cause refusal,
not an implicit broadcast termination. Never fetch recovery images from a registry.

Health is bounded to 90 seconds, checks exact compiled API/Agent version, actual
schema, Agent identity/worker image/mTLS, edge TLS and public TLS cabinet/control
reachability. It follows no public redirects, uses normal public certificate
verification and non-mutating private probes. Configuration fields alone do not
prove the running binary version: release builds embed the version in Go binaries.

Failure after INSTALLING or an interrupted uncertain mutation begins automatic
snapshot rollback. Verify the saved inventory, stop replacement components, preserve
failed DB/configuration diagnostics, load old images offline, restore the snapshot
under exclusive SQLite ownership, restore matching old files/Agent/UI, and verify
old health. A damaged target DB does not prevent restoring the saved good snapshot.
Restoration is repeatable after interruption. Preserve keys and owner/media settings;
revoke restored sessions/enrollment/recovery codes and fence current TOTP windows.
The owner may need to sign in again, wait up to 60 seconds and replace recovery codes.

COMMITTED/RECOVERED/ABORTED are durable decisions before admission opens. If the
process dies after opening admission but before the terminal result is written,
resume records the decision without rewinding new authentication/configuration data.
An interrupted preparation aborts/restarts the original installation; it never
blindly retries target installation. Failed recovery stays RECOVERY_REQUIRED with
maintenance closed and preserved diagnostics. New jobs are refused in that state.

One logical rollback may resume at most eight times and within 24 hours of queue.
Caught restoration/health failure does not retry automatically. systemd process
restart is separately bounded to three starts per ten minutes. Host/disk/Docker
failure and exhausted/failed recovery require administrative intervention.

## Administrative commands

The owner authorization primitive (migration 000007) atomically consumes fresh
TOTP and writes the release/request-bound receipt. Only one pending request is
allowed; 24-hour expiry and original owner/session authority apply at dispatch.
Snapshot restore, authenticator replacement and offline recovery cancel pending
requests. HTTP 202 is receipt acceptance, not proof of a running host job.

Manual installation requires independent verification/trust provisioning and begins
at sequence 0; signed Android installation records the verified release sequence.
Use the matching installed helpers, not scripts from an unverified download.

```sh
sudo bash update.sh /root/release.json /root/release.sig \
  /root/release.tar.gz 11111111-1111-4111-8111-111111111111
sudo python3 /usr/local/libexec/streamtool-updater/updater.py status

# Root-only preparation for the offline owner/API acceptance fixture:
sudo python3 /usr/local/libexec/streamtool-updater/updater_bridge.py offer \
  --manifest /root/release.json --signature /root/release.sig \
  --bundle /root/release.tar.gz
```

Use a new UUID per explicit attempt; retrying the same UUID/manifest returns the same
job, and another manifest with that UUID is rejected. The CLI queues and starts the
independent service; it does not infer success from API connection loss.

[Local Linux checks](LINUX_UPDATE_ACCEPTANCE.md) summarize verified host cases
and limits. No public update button is present.

## Allocated emergency space

Before PREPARING, the root coordinator records a private `reserve.json` inventory
and allocates mode-0600 per-job files with Linux `posix_fallocate`, fsyncing their
files/directories and checking allocated blocks. Journal, application, Docker's
reported storage, `/etc`, `/usr/local` and Agent-fence storage are deduplicated by
filesystem. Each filesystem needs working headroom plus a reserve equal to the
conservative recovery budget: twice installed image sizes, three times installed
file sizes, plus 2 GiB. On the local stand this reserve was about 3.14 GiB. Insufficient
headroom or unsupported allocation refuses the update before application replacement.

The snapshot stages beside its journal archive; restoration stages under the
application root. Neither depends on `/tmp` capacity. All staging remains private.
Below 64 MiB of root-available blocks, interrupted recovery frees the affected
reservation. An actual ENOSPC frees available job reservations and permits exactly
one additional engine resume from the durable phase. Partial installation resumes
rollback; a durable COMMITTED job retains its target and post-admission writes.
Terminal successful/rolled-back/aborted jobs release unused reserves. RECOVERY_REQUIRED
remains fenced and does not gain unbounded retries or a new reservation.

The independent `streamtool-updater-space.service` runs `release-space` after local
filesystems, before Docker. It is enabled for both multi-user and Docker startup,
and requires neither Docker nor an application health probe. It checks current and
accepted-owner journal reservations under the root coordinator lock. This prevents
Docker startup from blocking reserve release after a full-disk reboot. Reservation
paths are root-derived, never client input; private directories, no-follow file opens,
single-link checks and directory-FD unlink prevent path substitution. Only the job's
reservation files are deleted, never logs, media or user data.

Finite reserved capacity cannot defeat a writer that keeps consuming the released
space, inode exhaustion or device failure. Existing bounded recovery/fenced failure
rules still apply. See [actual full-disk and reboot checks](LINUX_UPDATE_ACCEPTANCE.md).
