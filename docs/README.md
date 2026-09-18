# Documentation

Start with [current status](STATUS.md): implemented features and verification limits. The project is **PRE-LAUNCH**.

## Use and operate

- [Self-host installation, backup, updates and recovery](SELFHOST.md)
- [Android build and distribution configuration](../apps/android/README.md)
- [Android SSH installation and owner enrollment](ONBOARDING.md)
- [Browser cabinet](CABINET.md)
- [Local development stack](SINGLE_NODE.md)
- [Operational runbooks](runbooks/README.md)

## Develop

- [Contributing](../CONTRIBUTING.md) and [tests](../tests/README.md)
- [Product contract](PRODUCT_ALIGNMENT.md) — authoritative scope and lifecycle
- [Architecture](ARCHITECTURE.md)
- [Owner API](../contracts/user-api.md)
- [GitHub CI](CI.md)

## Security and distribution

- [Security reporting](../SECURITY.md)
- [Cabinet authentication](CABINET_SECURITY.md)
- [Media runtime security](MEDIA_SECURITY.md)
- [Native release build and signing](RELEASE_BUILD.md)
- [Signed release verification](RELEASE_CHANNEL.md)
- [Host update/recovery coordinator](UPDATER_ENGINE.md)
- [Deployment boundaries](PRODUCTION.md)

## Local verification

[Linux recovery checks](LINUX_UPDATE_ACCEPTANCE.md) summarize verified host cases
and their limits. [Current status](STATUS.md) distinguishes local evidence from
real VDS, public TLS and Twitch verification.
