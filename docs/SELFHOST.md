# Self-hosted installation and maintenance

streamtool-relay is a pre-launch application for one owner on one Linux VDS. One
SRT/RTMP source is normalized by one continuous H.264/AAC encoder and sent to up to eight
independent RTMP/RTMPS destinations. On signal loss, an image or silent looped MP4 keeps the
output connected; recovery returns to the source. SQLite stores durable state.

## Host requirements

A candidate minimum is two CPU cores, 2 GiB RAM and 40 GiB disk. The worker reserves
2 cores and 1 GiB; the API, edge, Docker and OS need the remaining memory. This is a
candidate, not a verified Selectel tariff. Local ARM64 encoder measurements do not
establish the speed of shared x86 vCPUs. If 1080p60 cannot run in real time, lower
output quality or use a faster host. The source need not match output size/FPS.

Use a Linux host with systemd, Docker Engine/Compose v2, Python 3.12+, OpenSSL,
iptables/ip6tables, iproute2 (`tc`), util-linux (`nsenter`) and sudo. Installation
checks these dependencies before making changes. The Docker networking/kernel
must support namespace firewall rules and traffic policing; the worker fails closed
if policy installation fails. Standard Docker Desktop cannot establish host-policy
acceptance. Docker subnets 172.30.80.0/24, 172.30.81.0/24 and 172.30.82.0/29 must be free.
The builder and host must use compatible Docker image stores: a containerd-store
archive may import into a classic store without retaining its immutable IDs.
The installer verifies all four IDs after loading and starts Compose with
`--pull never`; it refuses mismatches before generating installation credentials.

Use a public static IPv4/IPv6 address, or optionally point a DNS hostname at the VDS.
The bundle configures public ACME for both modes; IP issuance/renewal still needs
real-server acceptance. Allow incoming TCP 80/443 for cabinet/certificate
issuance, UDP 443 optionally for HTTP/3, TCP 1935 for RTMP and UDP 8890 for SRT.
Reserve outbound bandwidth for every enabled output: eight 6000/160 kbps streams
need about 49.3 Mbps payload, with 20% headroom about 59.1 Mbps; payload is about
22.2 GB per hour. The default Agent budget is 100 Mbps. CPU is shared by the
encoder, but queues, muxers and connections consume memory per destination.
API, edge control and Agent have no published public ports. RTMP ingest is plaintext;
use SRT when the source supports it. Changing the default public ports also requires
changing deployment/public URLs consistently.

## Build and install

The no-terminal SSH installation protocol and first-configuration flow are described
in [automatic onboarding](ONBOARDING.md). SSH trust/checks, prerequisite preparation
and autonomous signed application deployment/native owner-setup handoff are
implemented. The default APK has no production release feed/key and cannot yet
perform application installation; actual host acceptance is still open. The commands below describe the existing manual installation path.

On a Linux build runner matching the host CPU architecture:

```sh
make selfhost-package VERSION="$STREAMTOOL_RELEASE_VERSION"
```

The archive contains worker/API images, a static Linux Agent, the matching website,
deployment scripts and a SHA256 manifest. Building does not publish a release.
Use the architecture-specific archive described in [release preparation](RELEASE_BUILD.md).
Transfer the matching signed bundle to the VDS and verify it with the independently
trusted verifier/public key before unpacking and executing scripts, then run:

```sh
sudo bash install.sh YOUR_PUBLIC_IP
# A DNS hostname is also accepted.
```

The installer verifies all package files, checks subnet conflicts, loads the images,
generates independent secrets and a private certificate authority, initializes
`/opt/streamtool`, and starts the stack and host Agent. It refuses to overwrite an
existing installation. It never reuses repository development credentials.

Open the printed HTTPS URL. Enter the printed installation code and a password,
scan the local QR code in an authenticator, save the ten recovery codes and confirm
a six-digit TOTP. There is no email field or mail dependency. Without the installation
code the first visitor cannot claim the server. Setup is a transactional singleton;
later setup attempts cannot create another owner. The
setup code remains in `/opt/streamtool/secrets/setup_token` for operator recovery;
it cannot create another account after setup. Public registration and billing are absent.

Enable the service in the cabinet. Save the source key when first issued; it is not
recoverable from the database. Add up to eight destinations with their server URLs and write-only stream keys.
Each destination retries independently; disabling it retains its settings and still
occupies a slot. Deleting it frees a slot. All destinations share one encoder,
quality and fallback. Defaults are 720p30/3000 kbps, immediately usable. Optional output quality is
changed before a broadcast, with publisher disconnected. Image/video upload has the
same inactivity requirement; 8-bit 4:2:0 H.264 Baseline/Main/High MP4 is limited to 30 seconds/50 MiB/1080p and its
audio is ignored. Clips are decoded directly; there is no preparation cache. Without automatic fallback,
NO_SIGNAL sends black video and silence to retain the RTMP connection.

## Isolation and certificates

Caddy terminates public HTTPS and automatically manages its public certificates.
IP deployments explicitly use Let's Encrypt's `shortlived` profile; DNS uses
`tlsserver`. IPv6 URL authorities are bracketed. Public ACME never falls back to
an internal issuer; issuance/renewal/reload and expiry monitoring remain acceptance
gates.
It verifies the private API certificate and blocks operator/edge administration
routes. [Caddy transport reference](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy).
MediaMTX verifies the private edge-auth certificate and exposes a TLS-only internal
control API. [MediaMTX configuration reference](https://mediamtx.org/docs/references/configuration-file).
The Agent requires TLS 1.3 and the controller client identity. Only the API sees
its controller certificate and encryption keys; the edge sees only its own leaf key.
Workers receive only session-scoped files through private tmpfs mounts, resource
limits and the namespace firewall. The Agent's Docker access is part of the host
trust boundary; its account is not an untrusted tenant.

Private leaf certificates last one year and the private CA ten years. A daily systemd
timer checks leaves 30 days before expiry, renews with the existing leaf keys/CA in an
idle window, then restarts API/Agent/edge. An active broadcast defers renewal; below
seven days remaining the service fails visibly so maintenance can be scheduled.
`journalctl -u streamtool-certificates` shows checks/errors. Forced renewal requires
workers to be removed; never renew or update in the middle of a stream. Private CA
renewal is a separate manual operation; the helper refuses issuing beyond CA validity.
The timer/host restart behavior still requires Linux acceptance rehearsal.

Never silently rotate the encryption key: old key IDs must remain available to decrypt
stored output secrets. The renewal helper changes only certificates, retaining keys.

## Backup

From a verified bundle directory:

```sh
sudo python3 backup.py backup /root/streamtool-backup.tar.gz
```

The command stops only the API, obtains the exclusive database lock, creates a
SQLite backup through its backup API, verifies integrity and archives database,
encryption key manifest/material, private certificates, installation configuration
and Agent fence watermark. Existing workers/edge continue streaming; configuration
and new sessions pause until the API restarts. The backup is mode 0600 and contains
credentials. Store a copy outside the VDS; application backups are not server backups.

## Update

The root-only offline path uses the independent signed update coordinator. Cabinet
controls and public discovery/feed are unfinished; complete Linux Docker/systemd
fault-injection coverage is incomplete. See [coordinator](UPDATER_ENGINE.md).
Provision the pinned public trust file
independently; no production key/feed is provided by this repository.

The restricted host bridge and owner TOTP API/outbox are implemented, with real
Linux root/UID-65532 IPC checked locally. Bootstrap keeps `UPDATE_OWNER_ENABLED=false`;
graphical controls and complete host acceptance are still missing. Do not enable
this switch for users before that acceptance. Root can prepare a signed offline
candidate with `updater_bridge.py offer`; see the coordinator's protocol and limits.

```sh
sudo bash update.sh /root/release.json /root/release.sig \
  /root/release.tar.gz 11111111-1111-4111-8111-111111111111
sudo python3 /usr/local/libexec/streamtool-updater/updater.py status
```

An update is refused during an active/unknown broadcast. The independent service
fences admissions, saves the complete old installation/offline OCI/database set,
installs the coherent target and checks actual private/public health. Failed or
interrupted mutation restores matching old files/images and the pre-update database;
old binaries are never deliberately run against the target schema. Recovery failure
keeps maintenance closed. Sessions/recovery codes restored from the snapshot are
revoked; keys remain unchanged. Existing installations without host-gate/version
metadata cannot use this path; do not bypass signature/fencing checks to update them.

## Restore

Restore is a maintenance operation. Stop the Agent and stack, disconnect sources,
and stop/remove all installation workers first (use their `streamtool.node` label,
not an unfiltered Docker command). `backup.py restore` refuses while services/workers
are present and makes another backup of the current installation before replacing it.

```sh
sudo python3 backup.py restore /root/streamtool-backup.tar.gz
```

The command verifies checksums/integrity, restores keys/configuration without rotating
them, marks previously active sessions ended with `MANUAL_RESTORE`, preserves history,
and requires a new publisher for a new broadcast. A stale snapshot must not resurrect
an already completed broadcast. Restore also revokes browser sessions, pending
enrollment and all recovery codes: a snapshot cannot know which were already used.
The encrypted authenticator stays unchanged. Restore fences the current TOTP window;
wait up to 60 seconds for a fresh code, then log in using TOTP and replace it to
generate fresh recovery codes, or use offline owner recovery if necessary.
It keeps the greater Agent high watermark; the
controller advances its durable fencing counter from the authenticated heartbeat.
Use the package/image versions corresponding to the backup, then start Compose and
`streamtool-agent` again. Test restore and certificate renewal on a Linux host before
public release; the local Docker media checks do not cover these host operations.

## Acceptance limits

This is pre-launch software. Local Linux update/rollback uses controlled certificates
and ephemeral keys; real VDS, public ACME, Twitch and full fault/load acceptance
remain open. Default APK builds cannot install the application until production
signed distribution is configured. See [current status](STATUS.md),
[production gates](PRODUCTION.md) and [Linux evidence](LINUX_UPDATE_ACCEPTANCE.md).

Project code uses [MIT](../LICENSE); dependency licenses/notices remain separate.
Historical PostgreSQL data/migrations are preserved, without automatic conversion.

## Owner access recovery (no email)

Login requires the password plus a TOTP or an unused recovery code. Server time must
be synchronized. A TOTP cannot be reused for another login; wait for a fresh code.
To replace a lost phone, log in with a recovery code, choose “Сменить аутентификатор”
and prove the password plus another unused recovery code. Save the new codes and
confirm the new authenticator. Confirmation revokes old sessions and codes.

If the password or all factors are lost, a host administrator can recover through
SSH. Run these commands in Bash from `/opt/streamtool` with installation access:

```bash
docker compose stop api
read -rsp 'New owner password: ' streamtool_owner_password; printf '\n'
printf '%s\n' "$streamtool_owner_password" | docker compose run --rm -T --no-deps api recover-owner
unset streamtool_owner_password
docker compose up -d api
```

The password is supplied over stdin rather than argv/environment. Use 12–256 bytes.
Recovery uses the exclusive SQLite lock: it refuses while the API owns the database.
It preserves media configuration, source/output/encryption keys and historical owner
metadata, changes the owner password and revokes sessions/authenticator/recovery codes.
Then open the cabinet and enroll fresh TOTP using the existing installation token
and new password. Rate limits still apply; recovery adds no password-only HTTP bypass.
Running media workers can continue while control is stopped, but new admission and
control recovery pause until API restart. Do not stop the Agent/edge for this action.

Existing pre-MFA owners must enroll using their existing password and installation
token after migration; their old browser sessions are intentionally revoked.

Updates now physically reserve recovery space before stopping the application.
The reserve is filesystem-specific and depends on installed files/images, with a
2-GiB fixed margin. Preflight
requires additional working headroom and refuses undersized disks. The reserve is
released automatically if interrupted recovery needs space and after terminal
completion. It is separate from user media/configuration and is never their deletion.
The independent early-space service releases critical reservations before Docker
startup after reboot. Snapshot staging uses protected journal/application disks.
Local full-disk/reboot tests passed; this does not guarantee recovery against ongoing
space consumption, inode exhaustion or failing storage. See
[coordinator behavior](UPDATER_ENGINE.md) and [exact evidence](LINUX_UPDATE_ACCEPTANCE.md).
