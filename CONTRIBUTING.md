# Contributing

streamtool-relay is pre-launch. Read the [current product scope](docs/PRODUCT_ALIGNMENT.md)
and [architecture](docs/ARCHITECTURE.md) before proposing changes. Current clients
share the [owner API](contracts/user-api.md); change clients, contracts and server
semantics together. Do not add compatibility shims for unreleased behavior.

## Development

Backend work requires Go (see `backend/go.mod`), Python 3, OpenSSL and Make.
Full checks also require Docker Engine with Compose/Buildx. Use a disposable Docker
host for media/network tests: they create containers, networks and test certificates.

```sh
make test vet race
make check
python3 tests/security/repository_hygiene.py
```

Android requires Go 1.26.8+, JDK 17+ and Android SDK 36:

```sh
make android-check
```

See [Android documentation](apps/android/README.md) for SDK configuration and
[system tests](tests/README.md) for media and emulator checks.

## Pull requests

Describe the concrete problem, resulting behavior and checks performed. State
whether evidence is a unit test, local real-media run or target-host verification.
Local fixtures do not prove Twitch or production acceptance. Keep security and
recovery guarantees intact; do not modify applied migrations or rotate stored keys.

Do not include `.artifacts`, build outputs, private configuration, SSH credentials,
signing seeds, certificates/private keys or user databases. Committed local fixture
credentials are public and intended only for isolated development. The repository
hygiene check guards the publication boundary; it is not a full secret scanner or
a substitute for reviewing the diff and repository history.

The project uses the [MIT license](LICENSE). Contributions are provided under
the same license. Avoid importing third-party code without
recording its source, license and required notices. Report vulnerabilities through
the [security policy](SECURITY.md), not a public issue containing secrets.
