# streamtool-relay Android cabinet

Native Kotlin/Jetpack Compose app, Android 8+, for one owner, their own VDS,
one SRT/RTMP source and up to eight independent RTMP/RTMPS outputs. Screens are bundled; there is no remote WebView/native command bridge.

The welcome screen offers “Подключиться” for an existing HTTPS cabinet and
“Настроить” for the IP/root-SSH wizard. Host identity and VDS checks run within
setup after “Продолжить”; there is no separate connection-test screen or action.
The SSH address field accepts an IP (default port 22), `IP:port` or `[IPv6]:port`;
there is no separate port field.

The UI uses Material 3 with the system light/dark theme and dynamic colors on
Android 12+. Setup and connection have a standard top app bar with a back arrow;
it follows the same navigation as Android Back. Content respects system bars
and the keyboard.

The current cabinet uses the same owner API as the website: password + TOTP or a
recovery code, setup/confirmation, authenticator replacement, routing enable/disable,
one-time source credentials and password-confirmed rotation, observed stop gating,
automatic/forced fallback, image/MP4 upload, output CRUD/retry and optional quality.
There is no email/public registration, balance, analytics, chat/moderation, Telegram,
Twitch OAuth or title management. First quality defaults are usable.

## Build

Go 1.26.8+ (for the two independent Linux verifiers), JDK 17+
(local Android Studio JBR 21), SDK 36, Gradle 9.1.0 wrapper and pinned
AGP/Compose dependencies. From this directory:

```sh
# Set JAVA_HOME and ANDROID_HOME to your local JDK and Android SDK.
./gradlew :app:assembleDebug :app:lintDebug :app:testDebugUnitTest
```

Install the debug APK with `adb install -r app/build/outputs/apk/debug/app-debug.apk`.
A release-signed [preview APK](https://github.com/IMP3RV70R/streamtool-relay/releases/tag/0.1.0-candidate.20260918.2) includes pinned signed installation distribution.
Default development builds remain unconfigured. The existing `dev.streamtool.app`
application ID and Keystore alias are retained; displayed product name is
`streamtool-relay`. This does not rotate stored encryption keys.

When an installation is detected during SSH setup, “Открыть кабинет” reads its
private setup handoff over pinned root SSH. It accepts only that same IP on HTTPS
443, verifies public TLS normally and supplies the installation code in memory
for first enrollment. The user still chooses an independent password, saves
recovery codes and confirms TOTP. No setup token is copied from a terminal or
persisted. The wizard now implements autonomous signed application deployment and resume,
but it is unavailable in unconfigured builds. Real SSH/Linux-host/ACME installation
acceptance remains open.

## Installer distribution configuration

The APK carries six fixed payload files: preparation/application/deployment Python
code, Linux verifiers for both architectures and public distribution configuration.
The build compiles the verifiers independently from the downloaded package. No
production release repository/key/feed has been supplied, so default builds carry
`{"enabled":false}` and explain that only dependencies can be prepared.

Once release infrastructure is established, supply a public JSON file at build time
with exactly `public_key` (base64 32-byte Ed25519 public key), `channel`
(`stable`/`preview`), `artifact_origin` (fixed HTTPS origin), `manifest_url` and
`signature_url` (HTTPS URLs on that same origin, no credentials/query/fragment):

```sh
./gradlew :app:assembleDebug -PinstallerDistribution=/absolute/public/distribution.json
```

This is publisher build configuration, not an extra server field for end users.
Never include a signing seed, credentials or enrollment token. The build and root
helper both validate it. GitHub Releases CDN redirects are constrained; signatures
and exact hashes stay mandatory. APK release signing is still not configured.

With configured distribution, after SSH identity/preflight the user sees a summary
and “Установить”. Progress comes from the autonomous host job, with three attempts
and a 45-minute deadline. Reconnect offers “Продолжить после переподключения”; a
failed phase offers a bounded explicit retry. Successful actual readiness triggers
owner enrollment automatically. Initial installation never claims update rollback
to a nonexistent previous version. See [host protocol](../../docs/ONBOARDING.md).

## Server and session security

Enter the cabinet's HTTPS origin; paths, queries, embedded credentials and fragments
are rejected. Debug builds additionally permit HTTP localhost/127.0.0.1/10.0.2.2.
Release builds require HTTPS with ordinary certificate verification; redirects are
rejected. There are no `/auth/native/*` aliases. Login and confirmation receive the
normal HttpOnly session cookie; the API client sends it only to its bound server.
Validate token shape, lifetime, cookie path/domain/HttpOnly and production Secure
attributes before storing it. Never turn the cookie into an arbitrary bearer token.

SessionStore encrypts the session/expiry with AES-256-GCM using Android Keystore and
server-origin associated data. Corrupt, expired or mismatched-origin state is cleared.
Passwords, TOTP/enrollment secrets, recovery codes and source/output keys are not
persisted in preferences or activity saved state. Screenshots and backup/device
transfer are disabled. Recovery-code export is explicit to a user-selected document;
source-key clipboard copying is explicit and marked sensitive.

Failed setup confirmation retains the in-memory challenge for retry. No session is
accepted before both factors pass. Authenticator replacement uses current password
and second factor, then a new TOTP confirmation. Logout clears local credentials
only after server revocation or an already-invalid session; network errors remain
retryable. Status errors/expired observations disable stop, and the server performs
its own final publisher-disconnection check.

Fallback uploads use a bounded stream (50 MiB), not an unbounded in-memory byte array.
The server validates codecs/size/duration and rejects changes during a session.
Generations are sent for all output/media/slate/fallback changes; stale writes fail.

## Verification

The [onboarding protocol](../../docs/ONBOARDING.md) implements signed resumable
SSH application deployment and native owner setup. Default builds disable application
installation until production distribution is configured. Browser relay and cabinet
update controls are absent. Real SSH/Linux/public ACME/VDS/Twitch
acceptance remains open; see [current status](../../docs/STATUS.md).

Local build/lint/unit and five emulator tests passed. Device HTTP fixtures verify
UI/cookie behavior, not real MFA correctness or deployment. Separate actual local
Go API integration verified password/TOTP, cookies, output generations and logout.

Run the five cabinet/navigation/handoff device tests with:

```sh
./gradlew :app:connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=dev.streamtool.app.CabinetTest
```

For optional real-server integration, build `:app:assembleDebugAndroidTest`, start
the owned local control stand, and run from the repository root:

```sh
ANDROID_HOME="$HOME/Library/Android/sdk" python3 tests/android/current_server.py \
  --origin http://127.0.0.1:18081 --owner-fixture .artifacts/control-owner.json
```

The deployment fixture must be private (0600/0400), contain `password` and
`totp_secret`, and never be committed. Use a single connected emulator/device.
Without the private fixture, the optional instrumentation class skips explicitly.
