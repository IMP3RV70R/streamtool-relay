# Local Linux recovery acceptance

These are local tests on disposable ARM64 Debian 13/systemd/Docker VMs, not Selectel
or production acceptance. HTTPS uses a controlled CA; release trust uses ephemeral
keys and an offline catalog. The table below summarizes the locally checked cases.

Fixtures: [tests/selfhost/linux_host](../tests/selfhost/linux_host/README.md).
Run them only in their documented isolated VM environment.

## Verified cases

| Area | Local result |
|---|---|
| Installation | Complete older bundle, private services/HTTPS and owner password/TOTP enrollment |
| Owner update | Fresh TOTP, real UID-65532 Unix bridge/systemd coordinator, exact image/schema readiness |
| Migration failure | Matching previous images/Agent/website/SQLite restored and verified automatically |
| Interrupted installation | SIGKILL + VM reboot resumed rollback without manual wake |
| Host-operation faults | Preparation, load/config/install/restore/commit/readiness and backup cases |
| Journal boundaries | Separate journal filesystem capacity and interrupted writes preserved durable state |
| Persistent VM stop | Forced guest termination resumed rollback or retained committed target on boot |
| Full root disk | Actual allocated ENOSPC tested in partial installation and final-status write |
| Emergency reserve | Full-disk recovery with filler retained; early boot release before Docker, bounded retry |

Keys/configuration and matching health were checked. Restored authentication
capabilities were revoked; replay/fencing authority did not decrease. Committed
post-admission mutations were preserved rather than rewound.

## Limits

These runs do not accept the current initial Android installer, public ACME, real
SSH/apt provisioning, native amd64 or target-VDS load. Historical bundle acceptance
must be repeated against the latest required runtime/`deploy.py` contract.

The tests do not cover the complete interruption/storage/service matrix, inode/device
failure, writers consuming released reserves, all failed-recovery paths, public signed
distribution, actual Twitch or a target-host 24-hour stream. Finite reserves do not promise
recovery against indefinitely full storage. Controlled virtual-disk termination is
not physical device power-loss proof. See [current status](STATUS.md) and
[production gates](PRODUCTION.md).
