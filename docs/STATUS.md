# Current project status

Updated: 2026-09-18. **PRE-LAUNCH**; a signed installation preview is published, with no accepted production release.
The [product contract](PRODUCT_ALIGNMENT.md) defines the current scope.

## Implemented and locally verified

| Area | Current implementation and evidence |
|---|---|
| Storage | Private SQLite/WAL/FULL, exclusive ownership, migration checksums, atomic quota/generations and controller/Agent fencing |
| Cabinet/API | Password + TOTP setup/login, recovery codes, factor replay protection, encrypted/hash-only secrets, CSRF and persistent attempt limits |
| Media | Continuous encoder, image/looped-MP4 fallback/return, eight outputs, isolated destination failure and explicit stop |
| Android | Native cabinet, two-action welcome screen, app-bar navigation, encrypted origin-bound session, SSH pins and setup handoff; build/lint, nine unit tests, five emulator tests and actual local API contract test passed |
| Initial installation | Signed download/staging, immutable images, bounded resumable systemd job and preserved configuration; 21 local installer tests passed |
| Software updates | Independent coordinator, restricted Unix bridge, owner TOTP API/outbox, matching-version/database rollback and emergency-space recovery |
| Distribution/security | MIT, GitHub checks/native candidate workflows, offline signing/verifier; current-format native GitHub amd64/arm64 packages and exact-image scans passed |

## Current limitations

Default Android builds disable application installation because no production
release distribution configuration is supplied. Dependency preparation is available.
Owner update routes are disabled by default. Graphical update controls, public
update-feed discovery and a landing-page browser installer are absent.
The repository is [IMP3RV70R/streamtool-relay](https://github.com/IMP3RV70R/streamtool-relay).
The signed [preview release](https://github.com/IMP3RV70R/streamtool-relay/releases/tag/0.1.0-candidate.20260918.2) includes amd64/arm64 bundles, an initial-install-only
manifest and a release-signed Android APK with pinned preview trust. Its metadata
expires on 19 October 2026. The ordinary builds remain unconfigured; no stable
production release or public update feed is configured.

Local Linux update/rollback uses controlled certificates and ephemeral keys.
Scans apply only to their exact images and database date.

Native GitHub Linux amd64 source checks, eight-output/image/video recovery and
image security, API integration and standalone media gates passed. Native amd64
and arm64 packaging and exact-image scans also passed on GitHub.

Actual SSH/apt/systemd initial deployment, public ACME issuance/renewal, initial deployment of native bundles,
Selectel capacity, real Twitch ingest and a 24-hour target-host run are unverified.
The full storage/service fault matrix and independent security audit are not covered.
Local checks do not establish production readiness.

## Procedures and verification

- [Installation and maintenance](SELFHOST.md)
- [Local Linux recovery checks](LINUX_UPDATE_ACCEPTANCE.md)
- [Media dependency security](MEDIA_SECURITY.md)
- [Native packaging and offline signing](RELEASE_BUILD.md)
