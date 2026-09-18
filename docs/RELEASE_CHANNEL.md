# Signed release verification

Implemented 2026-09-17: offline Ed25519 manifest verification, durable anti-rollback
watermark and bounded private bundle staging. This tool never executes package code,
loads images, changes the installed application or declares deployment health.
The root-only offline coordinator now uses this verifier and admission fencing; see
[its implementation](UPDATER_ENGINE.md). Initial-installer network download and native SSH job onboarding are now
implemented (see [onboarding](ONBOARDING.md)). Public release publication, update-feed discovery
and cabinet update controls remain unfinished.

## Trust boundary

Build `backend/cmd/release-tool` independently of a downloaded bundle. Establish its
provenance and the pinned public key through a trusted distribution path. Running an
unverified tool extracted from the same package would defeat signature verification.
The initial installer must establish this trust before executing any bundle scripts.
The initial offline install script is not automatically protected by this
new tool for initial installation; its checksums alone still do not authenticate the
publisher. The root administrative update wrapper now queues signed verified jobs.

The host trust file has exactly `public_key` (base64 32-byte Ed25519 key), `channel`
and `artifact_origin` (one fixed HTTPS origin). It belongs to the verifier's user and
is not writable by others. State and staging parent directories belong to that user
and are private (0700). Production runs under root. The signing seed is a separate
private base64 32-byte file outside the repository, installed hosts and bundles;
never deliver it to the API or clients. No production signing key/feed exists yet.

## Manifest protocol 1

Sign the exact JSON bytes; detached signatures are base64 Ed25519. Unknown fields,
duplicate JSON keys and trailing JSON are rejected. The manifest contains:

- `sequence`: positive monotonically increasing release sequence;
- `version`, `channel`, `protocol` (currently 1), public `notes`;
- `created`, `expires`: RFC3339 times, maximum validity 31 days; future creation
  beyond five minutes and expired manifests fail closed;
- `target_schema`: exact expected target SQLite schema, without schema downgrades;
- `minimum_sequence`, `minimum_schema`, `maximum_schema`: supported upgrade range;
- `artifacts`: one descriptor per supported architecture (`amd64`/`arm64`), with
  `architecture`, `url`, exact compressed `size` and lowercase hex `sha256`.

All artifact addresses use the configured HTTPS origin, without credentials, query
or fragment. The verifier selects the host architecture and rejects incompatible
installed sequence/schema/protocol. First installation explicitly uses `--initial`.
Release sequence does not depend on lexical ordering of version strings.

The independent watermark contains the greatest accepted signed sequence and exact
manifest SHA-256. Lower sequences and different content at the same sequence fail.
Identical retry is allowed. Watermark replacement uses fsync and atomic rename under
an exclusive OS lock; failed staging retains accepted metadata, preventing fallback
to an older feed. Keep this state outside application snapshots/rollback. Key rotation
and protocol upgrades are not implemented: do not replace trust through unsigned data.

## Offline commands

Build on the trusted release/host setup path:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go -C backend build \
  -trimpath -o /trusted/path/release-tool ./cmd/release-tool
```

Protected release infrastructure signs a prepared manifest:

```sh
release-tool sign --key /private/signing-seed \
  --manifest release.json --signature release.sig
```

A host verifies a previously downloaded package using its independently pinned trust:

```sh
release-tool verify --trust /etc/streamtool/release-trust.json \
  --state /var/lib/streamtool-updater/watermark.json \
  --manifest /private/release.json --signature /private/release.sig \
  --bundle /private/release.tar.gz \
  --destination /var/lib/streamtool-updater/staging/release-9 \
  --architecture amd64 --installed-sequence 8 --installed-schema 6
```

For first installation replace installed-state flags with `--initial`. Destinations
must be new directories. The CLI does not create parent directories or an HTTP API.
The host coordinator supplies installed state from its own trusted installation/journal,
not from browser request fields. Staging success is never installation authorization.

## Package handling and local evidence

Archive input is copied once into a private file while checking signed size/hash;
extraction reads this verified snapshot, avoiding a hash/reread input race. Limits:
8 GiB compressed, 16 GiB extracted, 4096 entries, bounded manifest/checksum metadata.
Reject traversal, duplicate names, links and special files. Files are staged as 0600
and directories as 0700; executable/setuid bits are discarded. Require complete
internal checksums, required application files, matching version/architecture and
immutable image IDs. On failure remove the incomplete stage; never overwrite one.
Current bundles must also contain the independent coordinator/bridge runtime,
verifier, maintenance fence and both systemd units. Missing runtime files are
rejected before executing installation scripts.
This is package consistency, not proof that OCI contents run or passed security gates.

`infra/selfhost/archive.py` builds portable regular-file archives without platform
xattrs or unchecked metadata. Earlier candidates lack the latest required runtime
and do not satisfy the current complete-bundle contract.

## Verification

```sh
go -C backend test -race ./internal/release ./cmd/release-tool
```

Optional actual-bundle integration requires `STREAMTOOL_RELEASE_TEST_BUNDLE`,
`STREAMTOOL_RELEASE_TEST_VERSION`, `STREAMTOOL_RELEASE_TEST_ARCHITECTURE` and
`STREAMTOOL_RELEASE_TEST_SCHEMA` matching a complete current-format bundle.
Without one, integration skips explicitly. Negative tests cover signature/key/expiry,
rollback/equivocation, schema/protocol/architecture, foreign URLs, duplicate JSON,
unsafe/missing archive entries, checksums and gzip corruption.

Native static builds and fixture staging do not establish Linux-host deployment.
[Release preparation](RELEASE_BUILD.md) verifies/stages both architectures before
producing signed metadata; no production publication or acceptance gate is bypassed.

The current initial-installer package contract additionally requires `deploy.py`,
matching resumable bootstrap/deployment code. Earlier locally accepted candidate
archives remain unchanged historical evidence and must be rebuilt for this contract.
No compatibility alias or unverified fallback package is accepted.
