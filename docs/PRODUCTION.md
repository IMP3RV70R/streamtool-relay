# Deployment boundaries

**PRE-LAUNCH.** Code, local tests and source publication do not establish production
acceptance. [STATUS.md](STATUS.md) records current implementation/evidence;
[PRODUCT_ALIGNMENT.md](PRODUCT_ALIGNMENT.md) defines the intended contract.

## Host and security boundary

One owner/VDS with private SQLite, isolated per-session worker and up to eight outputs.
Use independent generated secrets, private TLS/mTLS and the supported host policy.
Development fixtures never belong on a public server. Only HTTPS/certificate and
source-ingest ports are public; API internals, edge management and Agent stay private.
Docker access makes the host Agent an administrative trust boundary.

A candidate minimum is two cores/2 GiB RAM/40 GiB disk. It is not an accepted VDS
capacity claim; worst-case source complexity, shared vCPU contention and eight-output
bandwidth must be measured. See [installation requirements](SELFHOST.md).

The patched complete local ARM64 candidate passed exact scans with zero HIGH/CRITICAL;
remaining Medium findings and scope limits are recorded in [media security](MEDIA_SECURITY.md).
Every published build requires a fresh exact-image/binary review. Earlier passing
scans and unit tests do not waive new findings or missing native-architecture evidence.
MIT covers project code; third-party license/notice/source obligations remain separate.

## Verification limits

There is no public release, Selectel VDS acceptance or real Twitch test. The current
Android installer has local fixture coverage, without accepted real SSH/apt/systemd
initial deployment. Public TLS issuance/renewal and target-host resource/load behavior
are unverified. Local Linux recovery covers specific interruption/storage cases,
not physical device failure or the complete fault matrix.

Native amd64, production release signing/feed, APK release signing and third-party
distribution review are not configured or accepted. Default application deployment
and owner update routes remain disabled. There has been no independent security audit.
See [current status](STATUS.md) and [Linux checks](LINUX_UPDATE_ACCEPTANCE.md).
