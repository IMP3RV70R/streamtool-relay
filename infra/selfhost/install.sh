#!/usr/bin/env bash
# Run from an unpacked, verified release bundle on a Linux VDS.
set -euo pipefail
[[ $(id -u) == 0 && $(uname -s) == Linux ]] || { echo 'Run as root on a Linux VDS.' >&2; exit 1; }
[[ $# == 1 ]] || { echo 'Usage: sudo bash install.sh PUBLIC_IP_OR_HOSTNAME' >&2; exit 2; }
bundle=$(cd "$(dirname "$0")" && pwd)
for tool in docker python3 openssl systemctl iptables ip6tables tc nsenter sudo visudo; do
 command -v "$tool" >/dev/null || { echo "Missing prerequisite: $tool" >&2; exit 1; }
done
docker info >/dev/null
docker compose version >/dev/null
[[ -f "$bundle/SHA256SUMS" && -f "$bundle/images.tar" && -f "$bundle/node-agent" ]] || { echo 'Use a complete release bundle.' >&2; exit 1; }
(cd "$bundle" && sha256sum --check SHA256SUMS >/dev/null)
case $(uname -m) in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported host CPU.' >&2; exit 1;; esac
[[ $(cat "$bundle/ARCHITECTURE") == "$arch" ]] || { echo 'Bundle CPU architecture mismatch.' >&2; exit 1; }

[[ ! -e /opt/streamtool/api.env && ! -e /etc/streamtool/node.env ]] || { echo 'Installation exists; use update.sh.' >&2; exit 1; }
python3 - <<'PY'
import ipaddress,json,subprocess
ids=subprocess.check_output(['docker','network','ls','-q'],text=True).split()
if ids:
 networks=json.loads(subprocess.check_output(['docker','network','inspect',*ids]))
 desired=[ipaddress.ip_network(s) for s in ('172.30.80.0/24','172.30.81.0/24','172.30.82.0/29')]
 for network in networks:
  for config in (network.get('IPAM') or {}).get('Config') or []:
   subnet=config.get('Subnet')
   if subnet and ':' not in subnet and any(ipaddress.ip_network(subnet).overlaps(d) for d in desired):
    raise SystemExit('Docker subnet conflict: '+subnet)
PY
# Reject an invalid address before host files/services or images are changed.
public_address=$(python3 - "$bundle" "$1" <<'PY'
import sys
sys.path.insert(0, sys.argv[1])
import bootstrap
print(bootstrap.public_address(sys.argv[2])[0])
PY
)
# The initial bundle/tool must have independently verified provenance.
python3 - "$bundle" <<'PY'
import sys
sys.path.insert(0,sys.argv[1])
import maintenance
maintenance.initialize()
PY
install -d -m 0700 /var/lib/streamtool-updater/journal
install -d -m 0750 -o root -g 65532 /var/lib/streamtool-updater/socket
install -d -m 0755 /usr/local/libexec/streamtool-updater
install -m 0755 "$bundle/"{updater.py,updater_bridge.py,updater_engine.py,updater_host.py,maintenance.py,backup.py,release-tool} /usr/local/libexec/streamtool-updater/
install -m 0644 "$bundle/"{streamtool-updater.service,streamtool-updater-space.service,streamtool-updater-bridge.service} /etc/systemd/system/
# Verify before loading artifacts or changing configuration.
docker load -i "$bundle/images.tar" >/dev/null
for component in api worker proxy edge; do
 image=$(cat "$bundle/$component-image")
 [[ $image =~ ^sha256:[0-9a-f]{64}$ ]] && docker image inspect "$image" >/dev/null 2>&1 || {
  echo 'Bundle immutable image IDs unavailable after load; use a matching Docker image store.' >&2
  exit 1
 }
done
python3 "$bundle/bootstrap.py" /opt/streamtool "$1" \
 --api-image "$(cat "$bundle/api-image")" --worker-image "$(cat "$bundle/worker-image")" \
 --proxy-image "$(cat "$bundle/proxy-image")" --edge-image "$(cat "$bundle/edge-image")"
install -m 0644 "$bundle/VERSION" /opt/streamtool/VERSION
printf '0\n' > /opt/streamtool/RELEASE_SEQUENCE
getent passwd streamtool-agent >/dev/null || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin streamtool-agent
usermod -a -G docker streamtool-agent
install -d -m 0750 -o root -g streamtool-agent /etc/streamtool
install -d -m 0755 /usr/local/libexec
install -m 0755 "$bundle/node-agent" /usr/local/bin/node-agent
install -m 0755 "$bundle/streamtool-worker-policy" /usr/local/libexec/streamtool-worker-policy
install -m 0440 "$bundle/streamtool-agent.sudoers" /etc/sudoers.d/streamtool-agent
visudo -cf /etc/sudoers.d/streamtool-agent >/dev/null
install -m 0600 -o root /opt/streamtool/node.env /etc/streamtool/node.env
install -m 0600 -o root /opt/streamtool/worker-network.env /etc/streamtool/worker-network.env
install -m 0644 "$bundle/streamtool-agent.service" /etc/systemd/system/streamtool-agent.service
cp -a "$bundle/web" /opt/streamtool/web
chown root:streamtool-agent /opt/streamtool /opt/streamtool/certs
chmod 0750 /opt/streamtool /opt/streamtool/certs
chown 65532:65532 /opt/streamtool/state /opt/streamtool/secrets /opt/streamtool/keyring.json /opt/streamtool/media-node.json
chmod 0700 /opt/streamtool/state /opt/streamtool/secrets
chown 65532:65532 /opt/streamtool/secrets/*
for name in api controller; do
 chown 65532:65532 /opt/streamtool/certs/$name.key
 chmod 0600 /opt/streamtool/certs/$name.key
done
chown root:streamtool-agent /opt/streamtool/certs/agent.key /opt/streamtool/certs/ca.crt
chmod 0640 /opt/streamtool/certs/agent.key /opt/streamtool/certs/ca.crt
chmod 0644 /opt/streamtool/certs/*.crt
# Static networks must exist before the Agent binds their gateway address.
(cd /opt/streamtool && docker compose up -d --pull never)
install -d -m 0755 /usr/local/libexec/streamtool-maintenance
install -m 0755 "$bundle/backup.py" "$bundle/renew.py" "$bundle/maintenance.py" "$bundle/update.sh" /usr/local/libexec/streamtool-maintenance/
install -m 0644 "$bundle/streamtool-certificates.service" "$bundle/streamtool-certificates.timer" /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now streamtool-agent.service
systemctl enable streamtool-updater.service streamtool-updater-space.service
systemctl enable --now streamtool-updater-bridge.service
printf 'Open https://%s and create the owner account. Setup code:\n' "$public_address"
cat /opt/streamtool/secrets/setup_token

systemctl enable --now streamtool-certificates.timer
