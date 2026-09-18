# streamtool-relay

Keep a live stream running when its source disconnects. Send one SRT or RTMP source
to your own server; streamtool-relay switches to an image or looping video during
signal loss and returns automatically when the source recovers.

```text
SRT / RTMP source → source-loss protection → up to 8 RTMP / RTMPS destinations
                           ↑
                    image or looping MP4
```

## What it does

- One continuous H.264/AAC encoder, shared by independently recovering outputs.
- Image or silent looping-video fallback, with automatic and manual switching.
- Native Android app and browser cabinet for setup and stream control.
- Password + TOTP authentication, single-use recovery codes and private setup token.
- Local SQLite storage, encrypted destination keys and isolated session workers.
- Signed release verification and an independent host update/recovery coordinator.

Twitch is the primary target. Standard H.264/AAC output supports up to 1080p and
15–60 FPS, including fractional rates. Defaults are 720p30 at 3000 kbps.
Enhanced Broadcasting, HEVC, multiple video tracks and 1440p are outside current scope.

## Project status

**Pre-launch.** A signed [installation preview](https://github.com/IMP3RV70R/streamtool-relay/releases/tag/0.1.0-candidate.20260918.3) is available
for clean Debian 13 / Ubuntu 24.04 servers with public IP and root SSH. Download
`streamtool-relay-0.3.3-preview.apk`, choose “Настроить” and enter the SSH address
and credentials. Its signed installation metadata expires on 19 October 2026.
Default development APKs still disable deployment. Public update controls are unfinished.
Local media and recovery checks have passed; real VDS, public certificate issuance
and Twitch acceptance remain outstanding.

For deployment details, read the [self-host guide](docs/SELFHOST.md) and
[Android setup guide](apps/android/README.md). Installation generates private keys
and credentials; development fixtures must never be used on a public server.

## Run a local development stack

Use Docker Engine with Compose/Buildx, Make, Python 3, OpenSSL and Go. The backend
module declares its minimum Go version; Android verifier builds require Go 1.26.8+
with JDK 17+ and Android SDK 36. Containers build the media dependencies.

```sh
make control-up
```

Open <http://localhost:18080/dashboard>. The private first-setup token is generated
in `infra/local/secrets/setup_token`. Choose a password and enroll TOTP.

```sh
make check race              # backend, installer and static checks
make android-check           # Android build, lint and unit tests
make control-multistream-e2e  # eight outputs and independent failure recovery
make control-video-e2e        # looped-video fallback and source return
make control-down            # stop local services; retain database volumes
```

Media tests need a disposable Docker host and can be resource intensive.
See [contributing](CONTRIBUTING.md) for prerequisites and test boundaries.

## Explore the repository

| Directory | Contents |
|---|---|
| [`apps/android`](apps/android) | Kotlin / Jetpack Compose application and SSH onboarding |
| [`apps/web`](apps/web) | Browser cabinet |
| [`backend`](backend) | Go API/controller, Node Agent and media worker |
| [`contracts`](contracts/user-api.md) | Owner API contract |
| [`infra/selfhost`](infra/selfhost) | Installation, signed releases, backup and recovery |
| [`tests`](tests/README.md) | Media, installer, security and client checks |
| [`docs`](docs/README.md) | Architecture, operations and verification evidence |

GitHub Actions runs isolated checks and native candidate builds; it does not publish
or sign production releases. Historical PostgreSQL migrations remain preserved;
the active schema is in `backend/sqlite-migrations`.

Read the [security policy](SECURITY.md) before reporting vulnerabilities.

## License

[MIT](LICENSE). You may use, modify, distribute and sell the project while retaining
the copyright and license notice. Third-party dependencies retain their own licenses.
