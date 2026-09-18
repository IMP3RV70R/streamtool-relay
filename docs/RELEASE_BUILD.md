# Native bundles and offline signing

Implemented: architecture-specific native Linux packaging and a signing/preflight
command. No production seed, public feed, remote build or publication is configured.
The repository is `IMP3RV70R/streamtool-relay`. This process is not byte-for-byte
reproducible: base images, apt repositories and archive timestamps are not locked to
one immutable snapshot. It provides a repeatable checked release procedure.

Build the same reviewed source revision on separate native Linux amd64 and arm64
runners with working Docker and Go 1.25.14:

```sh
make selfhost-package VERSION="$STREAMTOOL_RELEASE_VERSION"
```

Output names bind version and architecture:
`.artifacts/streamtool-relay-VERSION-amd64.tar.gz` and
`.artifacts/streamtool-relay-VERSION-arm64.tar.gz`. Packaging refuses non-Linux hosts
and image-platform mismatches; it exports immutable API/worker/proxy/edge image IDs,
Agent, independently installed updater foundation, verifier, website and MIT LICENSE. Existing
outputs are never overwritten. Both archives must come from the same source revision.
Packaging alone does not attest security, actual installation or stream compatibility.

Native media sources are pinned by version and SHA256 in
`backend/scripts/media-sources.json`. The build enables only selected GStreamer
plugins and util-linux libraries, disables Meson fallback downloads and retains
source configuration/licenses in the worker. Other libraries retain Debian package
inventory. Pinning source archives does not freeze the underlying apt repositories.

Exact-image acceptance requires both the Debian/Go Trivy scan and a separate Grype
0.118.0 upstream SBOM scan. The latter validates six locked source identities,
hashes, configurations and licenses, detects deliberately vulnerable GStreamer and
util-linux controls, and requires the control/actual reports to use the same valid
database. HIGH/CRITICAL, unavailable/stale databases, missing reports or missing
coverage fail the gate. `source-coverage-control.json` contains deliberate fixture
findings, not shipped worker versions; `worker-upstream.json` is the actual result.
Scanners run without root, capabilities, Docker socket or secrets. SQLite temporary
scan data uses a temporary cache on the report filesystem, not host `/tmp` or the
bounded scanner tmpfs. Put reports on disk and allow room
for database download, hydration and temporary files as well as native build layers.

The protected signing machine independently builds `backend/cmd/release-tool` from
reviewed source. Provision an external mode-0600 base64 Ed25519 seed and the public
trust JSON through the trusted administration path. Do not create/store the seed in
the repository, Actions PR jobs, installed servers or output directory. The public
trust includes `public_key`, `channel` and `artifact_origin`; for GitHub Releases the
origin is `https://github.com`; the repository is `IMP3RV70R/streamtool-relay`.

Set version, unique increasing release sequence, compatibility bounds and target
SQLite schema explicitly. Do not infer compatibility from the latest migration count
or reset/reuse the published sequence. Example after those decisions are supplied:

```sh
python3 infra/release/prepare.py \
  --tool "$STREAMTOOL_TRUSTED_RELEASE_TOOL" \
  --key "$STREAMTOOL_EXTERNAL_SIGNING_SEED" \
  --trust "$STREAMTOOL_PUBLIC_RELEASE_TRUST" \
  --version "$STREAMTOOL_RELEASE_VERSION" \
  --sequence "$STREAMTOOL_RELEASE_SEQUENCE" \
  --minimum-sequence "$STREAMTOOL_MINIMUM_SEQUENCE" \
  --minimum-schema "$STREAMTOOL_MINIMUM_SCHEMA" \
  --maximum-schema "$STREAMTOOL_MAXIMUM_SCHEMA" \
  --target-schema "$STREAMTOOL_TARGET_SCHEMA" \
  --base-url "https://github.com/$STREAMTOOL_REPOSITORY/releases/download/$STREAMTOOL_RELEASE_VERSION" \
  --amd64 ".artifacts/streamtool-relay-$STREAMTOOL_RELEASE_VERSION-amd64.tar.gz" \
  --arm64 ".artifacts/streamtool-relay-$STREAMTOOL_RELEASE_VERSION-arm64.tar.gz" \
  --output ".artifacts/signed-$STREAMTOOL_RELEASE_VERSION"
```

The command hashes both archives, signs exact manifest bytes, and uses the trusted Go
verifier to verify signature/trust/expiry/compatibility and fully stage both archives.
It validates inventory, checksums, matching version/architecture and immutable image
IDs without executing bundle code. Private verification stages are removed; only
`manifest.json` and `manifest.sig` remain. Original archives are rehashed before
completion. Failure removes only the newly created metadata output and preserves
external keys/trust and existing outputs. Validation is offline; it does not prove
URLs exist, OCI contents run, or that either platform passed security/media acceptance.

Before signing/publishing a production release, run all CI and target-architecture
acceptance, review licenses, scan the exact exported images and settle the initial
installer/verifier provenance. Any HIGH/CRITICAL or missing-coverage result blocks publication. No gate is
disabled by this tooling. Keep published bytes immutable;
renewing an expired manifest requires a new sequence, not editing an accepted one.
Initial-installer GitHub downloads enforce bounded origin/CDN redirect
checks; public feed publication and update discovery remain unfinished.

Local integration uses ephemeral external test keys and tiny nonexecuted bundle
fixtures. Successful two-architecture signing/staging, wrong-key rejection,
architecture mismatch cleanup and refusal to overwrite signed output passed with
the actual Go tool. This is not native multi-architecture Docker release acceptance.

## Native GitHub candidates and exact-image scan

[Native candidate workflow](../.github/workflows/release-build.yml) is manual-only,
read-only and has no signing key/publication permission. It runs the same reviewed
checkout on native `ubuntu-24.04` (amd64) and `ubuntu-24.04-arm` (arm64), with independent
source checks, native packaging and an exact-image scan. Candidate archives upload
only if that job passes; reports upload even when vulnerability checks fail. A passing
single architecture is not approval to publish a two-architecture release. Separate
integration/media acceptance, licensing and protected signing still apply.

`infra/release/scan.sh BUNDLE_DIRECTORY` scans the four immutable IDs recorded by
the package, not rebuilt default tags. It exports each exact image separately and
runs pinned Trivy without Docker socket/host secrets, failing on HIGH/CRITICAL or
unavailable scan/database. Reports record the corresponding image IDs. The standalone
Agent and source dependency checks are covered by the existing source gates; this
image scan alone does not attest every shipped binary. BuildKit/Buildx is required
explicitly before packaging, preventing accidental legacy-builder fallback.

The native workflow has been linted locally; no successful GitHub run or native
amd64 acceptance is recorded. Local ARM hardware/isolated Debian VM tests arm64;
x86 emulation does not establish native amd64 acceptance. No production key or
published release feed exists.

## Local evidence and current contract

The patched complete native ARM64 candidate passed exact scans and actual trust/staging
integration locally.
No native amd64 or GitHub run has been accepted. Earlier candidate archives remain
unchanged and lack the latest required `deploy.py`; rebuild before current acceptance.
See [current status](STATUS.md). Third-party notices/source obligations remain separate
from the root MIT license; protected production signing is not configured.
