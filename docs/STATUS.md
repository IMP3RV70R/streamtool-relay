# Current project status

Updated: 2026-09-18. **PRE-LAUNCH**; no public release has been published.
The [product contract](PRODUCT_ALIGNMENT.md) defines the current scope.

## Implemented and locally verified

| Area | Current implementation and evidence |
|---|---|
| Storage | Private SQLite/WAL/FULL, exclusive ownership, migration checksums, atomic quota/generations and controller/Agent fencing |
| Cabinet/API | Password + TOTP setup/login, recovery codes, factor replay protection, encrypted/hash-only secrets, CSRF and persistent attempt limits |
| Media | Continuous encoder, image/looped-MP4 fallback/return, eight outputs, isolated destination failure and explicit stop |
| Android | Native cabinet, two-action welcome screen, app-bar navigation, encrypted origin-bound session, SSH pins and setup handoff; build/lint/unit and five emulator tests passed |
| Initial installation | Signed download/staging, immutable images, bounded resumable systemd job and preserved configuration; 21 local installer tests passed |
| Software updates | Independent coordinator, restricted Unix bridge, owner TOTP API/outbox, matching-version/database rollback and emergency-space recovery |
| Distribution/security | MIT, GitHub checks/native candidate workflows, offline signing/verifier; complete patched local ARM64 candidate scanned with zero HIGH/CRITICAL |

## Current limitations

Default Android builds disable application installation because no production
release distribution configuration is supplied. Dependency preparation is available.
Owner update routes are disabled by default. Graphical update controls, public
update-feed discovery and a landing-page browser installer are absent.
The repository is [IMP3RV70R/streamtool-relay](https://github.com/IMP3RV70R/streamtool-relay).
Production release key/feed and APK release signing are not configured.

Local Linux update/rollback uses controlled certificates and ephemeral keys.
Earlier candidate archives lack the latest `deploy.py` contract and are not accepted
as current complete bundles. Scans apply only to their exact images and database date.

Actual SSH/apt/systemd initial deployment, public ACME issuance/renewal, native amd64,
Selectel capacity, real Twitch ingest and a 24-hour target-host run are unverified.
The full storage/service fault matrix and independent security audit are not covered.
Local checks do not establish production readiness.

## Procedures and verification

- [Installation and maintenance](SELFHOST.md)
- [Local Linux recovery checks](LINUX_UPDATE_ACCEPTANCE.md)
- [Media dependency security](MEDIA_SECURITY.md)
- [Native packaging and offline signing](RELEASE_BUILD.md)
