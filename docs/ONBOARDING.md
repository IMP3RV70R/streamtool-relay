# Android installation and first configuration

The Android wizard uses public IP/root SSH for installation and native owner setup.
Its host allowlist is clean Debian 13 or Ubuntu 24.04. A purchased domain is not
required by the protocol.

The protocol is implemented and enabled in the release-signed
[preview APK](https://github.com/IMP3RV70R/streamtool-relay/releases/tag/0.1.0-candidate.20260918.2). It remains **disabled in default builds** without distribution configuration. Real SSH/apt/systemd/reboot/public ACME and the
complete target-server flow remain unaccepted. See [current status](STATUS.md).

## User flow

1. Choose “Подключиться” for an existing HTTPS cabinet or “Настроить” for SSH setup.
2. Enter the public IP/root password. Default SSH port is 22; use `IP:port` or
   `[IPv6]:port` for another port. There is no separate port or connection-test screen.
3. Confirm the first-use SSH host fingerprint. Pins are bound to canonical IP/port;
   a changed key shows old/new fingerprints and can be explicitly confirmed after
   verification, allowing setup after a VDS reinstall. Credentials are withheld
   until confirmation; rejection preserves the existing pin. Read-only host checks run
   within setup and identify unsupported/conflicting hosts.
4. Review the installation summary and start the fixed host job. Progress is read
   from its journal; reconnect attaches to the same job. A fresh connection needs
   credentials again. Existing installations offer connection/resume, not blind reset.
5. After actual readiness, open the same-IP HTTPS cabinet through the native handoff.
   Choose a separate owner password, save ten recovery codes and confirm TOTP.
6. Enable routing, save the one-time source credentials, add a Twitch ingest URL/key
   and optionally select image/looped-video fallback. Quality defaults are usable.

The steps describe the configured implementation. An unconfigured debug APK cannot
deploy the application. Distribution build configuration is in the
[Android README](../apps/android/README.md).

The changed-key confirmation flow is implemented in the current source and locally
verified; it is not included in the published 0.3.1 preview APK.

## Trust and credentials

Android connects directly over pinned SSH. The root password remains only in
ViewModel memory until an explicit operation consumes it; leaving the idle wizard
clears it. Credentials are never journal fields, shell parameters or logged values.
Remote cabinet content has no native SSH/arbitrary-command bridge.

The APK includes fixed preparation/application/deployment code, two independently
built Linux release verifiers and public distribution configuration. Delivery accepts
exactly six SHA256-bound payload files, with private atomic publication/locking;
links, duplicate/unsafe names, oversized contents and foreign ownership are refused.
An active job cannot have its payload replaced. Requests contain only the public IP;
trust and release URLs come from the APK, not runtime user input or cabinet content.

Metadata is Ed25519-verified before downloading the architecture-selected bundle.
Expiry, exact hash/size and durable anti-rollback state are enforced. HTTPS downloads
are bounded and ignore environment proxies. Redirects remain on the configured
origin; GitHub may redirect only to its exact release-assets CDN over HTTPS.
Temporary CDN queries are not persisted/logged. Independent Go verification stages
without executing package code; accepted inventory is checked before execution.
See [release trust](RELEASE_CHANNEL.md).

## Installation diagnostics

The Android wizard displays the current/resume stage, attempt count and a safe
failure code. “Получить диагностику” reads job/Docker service state, exit status,
Docker availability, disk/inode capacity and available RAM/swap through pinned SSH.
It also works with an existing failed preview job without replacing its helper.
“Скопировать диагностику” copies only allowlisted observations and identifiers;
no passwords, setup tokens, output keys, command arguments or raw provider logs.
A snapshot is collected automatically at image import and after an observed error.
Snapshots describe the time of collection, not proof of the original cause.

New installer helpers persist safe timeout/command/exit/errno details. Python
imports do not create bytecode caches inside accepted staging. A retry can repair
legacy added Python-cache files only by independently verifying/restaging the
original signed bundle. Changed/missing signed files, symlinks or arbitrary added
files remain refused. Committed configuration, owner and keys are preserved.

Classic Docker and containerd image stores may expose different immutable IDs for
the same archive. Image import resolves either the signed config ID or the matching
OCI manifest digest derived from the signed archive; it never falls back to mutable
tags. The selected identities are persisted for configuration and readiness checks.

## Host job and recovery

The application job has a private atomic journal, exclusive lock, stable UUID and
independent systemd service/timer. Durable wake precedes PENDING. Stages:

```text
PREPARE → METADATA → DOWNLOAD → STAGE → IMAGES → CONFIGURE → HOST → START → READY
                                                                        ↓
                                                                   SUCCEEDED
```

Three automatic attempts and a 45-minute deadline bound execution. Explicit retry
continues the last failed stage with another bounded budget. Closing the app or
losing SSH does not own the service lifetime. Terminal errors are secret-free;
never infer success from disconnection.

Preparation checks the host, installs missing prerequisites and official
signed-repository Docker, verifies kernel namespace/policy support and refuses
competing packages/repositories. Existing Docker is checked, not replaced. No
OS-wide upgrade, reboot, firewall flush or removal of another workload.
**PREPARED means dependency readiness, not application installation.**

Loaded image platforms/IDs must match. Configuration and crypto are built in a
job-marked private directory on `/opt`'s filesystem; valid partial keys/tokens are
reused. Coherent configuration is fsynced and published with Linux rename-NOREPLACE.
A committed installation is never bootstrapped again. Foreign files, identities,
networks and unmarked stages are refused rather than claimed or overwritten.
Unused private temporary directories left before stage publication are not deleted
as if they were owned. First installation has no previous version for rollback;
initial resume and [software-update recovery](UPDATER_ENGINE.md) are distinct.

HOST installs matching Agent/policy/updater/renewal components. START uses preloaded
immutable images. READY checks version/schema/worker image, private API/Agent/edge
and ordinary public HTTPS cabinet/control readiness. Only SUCCEEDED permits handoff.
These checks do not establish that Twitch received a real stream.

## HTTPS and native owner handoff

Bootstrap accepts public IPv4/IPv6 and optional DNS. Caddy uses public ACME with
`shortlived` for IP and `tlsserver` for DNS, persisting renewal state. IPv6 HTTPS
origins use brackets. The source configuration uses an unbracketed
`TLS_SERVER_NAME` as Caddy `default_sni` so IP clients without SNI receive the
correct certificate. Actual proxy TLS tests cover IPv4/IPv6 without SNI, DNS and
rejection of a wrong certificate identity. This configuration fix is not included
in the currently published preview bundle. No internal issuer or TLS-verification bypass substitutes for
public issuance failure. IP certificate issuance was observed in target-host logs; complete HTTPS readiness,
renewal/reload and expiry recovery still require real-host acceptance. See [host requirements](SELFHOST.md).

The fixed root command reads private setup authority over authenticated SSH only
if no owner exists; an installer journal must be SUCCEEDED. Android accepts only
the same literal IP on normally verified HTTPS 443 and closes SSH before enrollment.
The setup token stays in memory, is never displayed/persisted/in a URL, and is cleared
on session acceptance, owner detection, server change or ViewModel destruction.
No session exists before password/TOTP confirmation. Failed confirmation remains
retryable. A separate browser visit uses ordinary owner login, never a URL-carried session.

## Verification limits

Local evidence: 27 application-installer tests and two diagnostics tests, independent Go signature/staging refusal checks,
Android build/lint/unit and five emulator cabinet/navigation/handoff tests. Fixture
host/network operations do not verify real SSH/Linux deployment, public ACME,
provider reachability, storage faults or actual Twitch delivery. No real VDS
installation-to-stream run has been accepted. See [current status](STATUS.md).
