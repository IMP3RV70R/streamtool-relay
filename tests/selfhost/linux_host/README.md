# Disposable Linux host acceptance

This opt-in fixture uses real systemd, Docker, HTTPS, SQLite and the independent
root coordinator. It is not part of `make check`. Never run `guest.py` or
`provision.sh` on a user server: they require the hostname
`lima-streamtool-update` and contain test-only provisioning.

Use Lima with `LIMA_HOME` in a private temporary directory and `lima.yaml`.
The VM has no host mounts, agent forwarding, host Docker socket or application
port forwarding. Provision Docker with `provision.sh` inside the VM; use the
containerd image store when importing archives produced by that store.

Build static Linux ARM64 API and Agent binaries for `host-test-old`,
`host-test-new` and `host-test-bad`, setting the matching buildinfo version.
Place them as `build/api-{old,new,bad}` and `build/agent-{old,new,bad}` in a private
working directory, together with `build/release-tool`. `prepare.py DIRECTORY`
builds uniquely named test API images using the locally available rc8 base and
unchanged media images. Its new fixture adds migration 8; its bad fixture adds
an intentionally invalid migration 9. No migration in the repository is changed.
Transfer the resulting payload into `/root` in the guest without shared mounts.

Inside the guest, run these actions in order:

```sh
sudo python3 /root/streamtool-host-acceptance/guest.py init
sudo python3 /root/streamtool-host-acceptance/guest.py success
sudo python3 /root/streamtool-host-acceptance/guest.py bad
sudo python3 /root/streamtool-host-acceptance/guest.py reboot
# After the guest returns:
sudo python3 /root/streamtool-host-acceptance/guest.py after-reboot
```

`init-resume` is only for continuing interrupted fixture enrollment after actual
host health passes; it refuses any existing owner. `init-retry` refuses existing
application configuration. Neither is an installer recovery mechanism.

Credentials, signing seed and private installer logs stay in root-only guest
files. Never print the private installation log: it can include the setup token.
Only PASS markers and fixed diagnostic statuses belong in exported evidence.
Repeated fixture runs reuse an identical prepared signed candidate and wait for
the durable authentication budget to expire. Reboot acceptance also checks a
changed kernel boot ID and rejection of the restored old cookie. Stop the VM
after testing.

TLS uses a controlled CA with normal certificate and hostname verification.
Release signatures use an ephemeral pinned test key with offline root catalog
preparation. This does not test public CA issuance, a published release feed,
production signing-key custody, Twitch, Selectel performance, every failure phase,
disk-full recovery or the complete release fault matrix.

After schema-8 acceptance, copy `matrix.py` beside `guest.py` and run the opt-in
root host-operation matrix:

```sh
sudo python3 /root/streamtool-host-acceptance/matrix.py \
  prepare_stop prepare_after install_mid install_after verify_before \
  restore_mid restore_after rollback_verify_before \
  commit_before_release commit_after_release backup_full load_failure \
  config_failure rollback_failure false_health
```

The matrix queues newly signed offline fixture releases through the trusted root
queue and executes the real LinuxHost operations. Child processes exit abruptly
at selected boundaries, and a fresh normal coordinator resumes. No fault switch is
added to installed application/runtime code. Owner/API authorization is covered by
`guest.py`; the matrix deliberately exercises the administrative transaction path.

`backup_full` mounts a private 4-KiB tmpfs only over the current fixture recovery
directory and verifies actual ENOSPC; it does not fill the system disk. Docker-load
failures use actual Docker against an empty input. `config_failure` writes invalid
fixture Compose after target replacement. `false_health` probes the real HTTP-200
API with mismatched version metadata and accelerates only the retry deadline.

`rollback_failure` must reach RECOVERY_REQUIRED, retain admission and perform no
further automatic retry. Afterwards an explicit fixture administrator removes the
injected fault and resumes rollback; that repair is not automatic acceptance and
is not a public recovery command. Verified terminal fixture copies are deleted to
bound test disk use, while their journals remain. Never apply this cleanup to a
user server.

For journal-capacity and state-write boundaries, `storage.py` uses the same private
fixture and trusted root queue. Run `storage.py primitives`, then for example:

```sh
sudo python3 /root/streamtool-host-acceptance/storage.py transaction \
  --point VERIFYING --mode full
```

Accepted full-journal points are PREPARING, INSTALLING, VERIFYING, COMMITTED,
SUCCEEDED, ROLLING_BACK, ROLLBACK_VERIFYING and RECOVERED. Process-exit checkpoints
were also verified before/after VERIFYING and COMMITTED, and after RECOVERED.

The journal moves to a private 1-GiB tmpfs only after the real idle/headroom check,
simulating capacity loss during the transaction; staged input stays bind-mounted
from its verified original tree. Filling this filesystem produces actual ENOSPC.
The fixture removes its filler and invokes the normal independent coordinator,
checks actual matching health/configuration/authentication, then preserves the
terminal journal on its original filesystem and unmounts only its own mounts.
An ENOSPC at the final SUCCEEDED write also checks that a post-admission SQLite
mutation survives. This does not fill the OS/application filesystem or test power
loss of persistent journal storage. Never reboot with this temporary mount active.
On unexpected failure, keep admission closed, inspect the fixed journal state,
remove only the fixture filler and resume recovery before unmounting.

Persistent-disk abrupt-stop checks use `power.py`. With only the isolated fixture
VM running and no `storage.py` tmpfs mounts active:

```sh
sudo python3 /root/streamtool-host-acceptance/power.py arm install_mid
# On the host, with LIMA_HOME pointing only to this fixture:
limactl stop --force streamtool-update
limactl start streamtool-update --tty=false
# Back in the guest:
sudo python3 /root/streamtool-host-acceptance/power.py verify
```

Repeat with `arm committed`. The child durably records its checkpoint and stops
itself while holding the update lock; never send SIGCONT. Forced VM stop destroys
that process. Verification invokes no coordinator: the installed boot-enabled
systemd unit must finish the job. It checks changed boot ID, matching installed
version/schema/sequence, preserved settings/keys, nondecreasing TOTP and revoked
restored capabilities after rollback. Checkpoint evidence stays root-private.
This simulates abrupt guest loss, not physical host/storage power loss.

Whole-root-filesystem checks use `system_disk.py --case install_mid` and
`system_disk.py --case terminal`, only inside the guarded disposable guest. Unlike
`storage.py`, these allocate the actual ext4 root filesystem, including its root
reserve. The script requires application, journal and Docker storage on that same
filesystem and uses one private mode-0600 filler. Allow up to the VM's 24-GiB disk
allocation on the host. Do not interrupt or reboot this supervisor while full.

The child must return actual ENOSPC from the update transaction; later free-space
measurements are unsuitable evidence because background cleanup can free blocks.
The supervisor checks that the journal remains complete and admission matches its
recorded phase, removes only its filler in `finally`, and wakes the normal systemd
coordinator. This wake is explicit fixture administration after capacity repair,
not evidence of recovery while storage remains full or autonomous disk cleanup.
The terminal case also commits a new SQLite row after admission reopens and checks
that final-status repair preserves it. Root-private evidence remains on the guest.

Allocated-reserve acceptance uses `system_disk.py --case install_mid --automatic`
and `system_disk.py --case terminal --automatic` with the current independent root
runtime installed. These invoke the production `updater.main()` path with only the
fixture's filesystem checkpoint injected. The filler must remain allocated through
terminal health/data verification; no manual capacity release or coordinator wake
participates. Cleanup deletes the fixture filler only after checks. On failure the
supervisor removes its own filler but preserves the journal/fence for inspection.

`power.py arm disk_full` durably records its checkpoint, fills the actual root disk,
and pauses the coordinator after `.env` replacement. Wait for ARMED, then force-stop
and restart only this VM. `power.py verify` waits for normal boot rollback, checks
successful early-space-service completion before Docker's start timestamp, and
removes the persistent filler only after health/configuration/key verification.
Install/enable the current root `streamtool-updater-space.service` first. This is
abrupt guest termination, not physical storage power-loss acceptance.
