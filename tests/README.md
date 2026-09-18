# Tests

Run commands from the repository root. Go tests live alongside their packages in
`backend/`; fixtures and generated reports belong in ignored `.artifacts/`.

| Command | Scope | Requirements |
|---|---|---|
| `make test vet race` | Go behavior, static analysis and race detector | Go; C compiler for race detector |
| `make check` | Backend, installer/recovery regressions and Compose validation | Go, Python 3, OpenSSL, Docker Compose |
| `make security` | Authentication, encryption, network policy and vulnerability checks | Go, ripgrep, network for advisory data |
| `python3 tests/security/repository_hygiene.py` | Files and credential patterns in the publication set | Python 3, Git |
| `make control-db-test users-test` | SQLite control/API integration | Go |
| `make control-e2e` | Actual media and process recovery | Disposable Docker host |
| `make control-multistream-e2e` | Eight outputs and isolated destination failure | Disposable Docker host |
| `make control-video-e2e` | Decoded looped-video fallback and source return | Disposable Docker host |
| `make security-images security-network` | Exact image scanning and network isolation | Docker, scanner downloads |
| `make android-check` | APK build, lint and JVM tests | Go 1.26.8+, JDK, Android SDK |
| `make android-device-test` | Instrumented Android workflow tests | Dedicated emulator/device |
| `make soak-24h` | Long-running local media scenario | Disposable Docker host, 24 hours |

`tests/selfhost` covers signed staging, initial installation, update/recovery and
backup behavior. `tests/selfhost/linux_host` contains separate disposable Linux
host acceptance fixtures. `tests/web` covers browser workflows; `tests/android`
contains cross-client/API acceptance tooling.

Run destructive fixtures only in their documented isolated environment. Emulator
checks must target a dedicated test device. Local checks, mock APIs and controlled
certificate authorities do not establish real Twitch, public ACME or VDS acceptance.
See [verification evidence](../docs/PRODUCT_ALIGNMENT.md) for current limits.
