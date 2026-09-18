# Infrastructure

- `selfhost`: signed installation, backup, update/recovery and packaged runtime.
- `release`: offline release preparation and exact packaged-image scanning.
- `ci`: shared isolated Linux runner bootstrap.
- `local`: MediaMTX configuration and deterministic development credentials for Compose.
- `vm`: service units and host configuration examples.
- `monitoring`: dashboards and alert rules.
- `regional`: example regional topology and load-balancer configuration.

The two Compose entrypoints stay at the repository root so existing `make` commands,
relative bind mounts and `.artifacts` paths have one stable base directory. Backend
images are built from `backend/`. Application-specific build tooling belongs with its application; shared deployment
infrastructure belongs here.

Files in `local/secrets` are development fixtures, not production credentials.
