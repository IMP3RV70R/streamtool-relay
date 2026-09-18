# GitHub Actions CI

Repository: [IMP3RV70R/streamtool-relay](https://github.com/IMP3RV70R/streamtool-relay).
GitHub is the repository, CI and release provider. Configuration:
[`.github/workflows/ci.yml`](../.github/workflows/ci.yml).
Shared Linux bootstrap: [`infra/ci/run.sh`](../infra/ci/run.sh).

Pushes, pull requests and manual dispatch run five independent Ubuntu 24.04 jobs.
Each job uses its own Docker-enabled hosted runner. Failures do not cancel the other
suites; a newer run on the same ref cancels the older run.

| Suite | Checks |
|---|---|
| `checks` | `make check race security` |
| `media` | `make e2e-fast` |
| `integration` | `make integration security` |
| `acceptance` | `make control-db-test users-test control-e2e control-video-e2e` |
| `security-images` | `make security-images` (release image vulnerability gate) |

Checkout is pinned to the verified v6.0.2 commit and does not persist credentials.
All CI jobs have read-only repository contents permission, no signing key and no
production credentials. Fork contributions use `pull_request`, never privileged
`pull_request_target`. These settings follow the
[GitHub Actions workflow reference](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax).

The bootstrap installs missing host tools, extracts Go 1.25.14 from its versioned
official image when needed, and obtains missing Compose/Buildx plugins from the
versioned Docker CLI image. Docker Engine must already work. Job timeout is 40
minutes; suite commands retain their 15/20/30-minute bounds and cleanup always runs.
Tests use local fixtures and temporary certificates. Image security failures remain
blocking; no vulnerability waiver was added by this migration.

GitHub Releases is the intended destination for signed amd64/arm64 bundles,
`manifest.json` and its detached Ed25519 signature. Publication/download automation
is not implemented by this CI workflow. No workflow has production signing or publication credentials; PR jobs do not
sign or publish releases. The existing pinned-key, expiry, hash/size and monotonic
sequence checks remain authoritative; GitHub release metadata alone is not trust.

Local workflow/shell validation is not a successful remote Actions run. No remote execution/publication occurred here.

A separate [native candidate workflow](../.github/workflows/release-build.yml) now
provides manual read-only amd64/arm64 packaging and exact exported-image scanning.
It uploads passing candidates and failure reports as short-lived Actions artifacts;
it does not sign, create GitHub Releases or replace production acceptance. Shared
checks now lint workflows with pinned actionlint before the existing test gates.
