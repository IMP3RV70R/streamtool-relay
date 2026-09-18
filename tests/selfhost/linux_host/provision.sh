#!/usr/bin/env bash
# Only the disposable VM. Official Docker packages, no convenience curl|sh script.
set -euo pipefail
[[ $(id -u) == 0 && $(hostname) == lima-streamtool-update ]] || exit 1
[[ ! -e /opt/streamtool/state/control.sqlite ]] || { echo 'Refusing pre-owner reset with any database.' >&2; exit 1; }
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl python3 openssl iptables iproute2 sudo
install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
chmod 0644 /etc/apt/keyrings/docker.asc
cat > /etc/apt/sources.list.d/docker.sources <<'EOF'
Types: deb
URIs: https://download.docker.com/linux/debian
Suites: trixie
Components: stable
Architectures: arm64
Signed-By: /etc/apt/keyrings/docker.asc
EOF
apt-get update -qq
DEBIAN_FRONTEND=noninteractive apt-get remove -y docker.io docker-cli docker-compose docker-buildx containerd runc
DEBIAN_FRONTEND=noninteractive apt-get install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
install -d -m 0755 /etc/docker
printf '%s\n' '{"features":{"containerd-snapshotter":true}}' > /etc/docker/daemon.json
systemctl restart docker
# Retain the failed, never-owned fixture installation privately for diagnostics.
# There is no application DB, owner, publisher or user data; this is test cleanup.
if [[ -d /opt/streamtool ]]; then
 mv /opt/streamtool /var/lib/streamtool-host-acceptance/failed-before-owner
fi
if [[ -d /etc/streamtool ]]; then
 mv /etc/streamtool /var/lib/streamtool-host-acceptance/failed-before-owner-etc
fi
docker info --format '{{.ServerVersion}} {{.Driver}}'
