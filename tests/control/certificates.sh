#!/bin/sh
set -eu
python3 - <<'PYSECRET'
from pathlib import Path
import secrets
import os, json, uuid
node = os.getenv("CONTROL_TEST_NODE_ID", "00000000-0000-4000-8000-000000000002")
if str(uuid.UUID(node)) != node or node == "00000000-0000-4000-8000-000000000001":
 raise ValueError("CONTROL_TEST_NODE_ID must be canonical and separate from the self-hosted node")
Path(".artifacts").mkdir(exist_ok=True)
Path(".artifacts/control-media-nodes.json").write_text(json.dumps([{
 "id":node,"agent_url":"https://agent:8443","slots":1
}])+"\n")
path=Path('infra/local/secrets/setup_token')
if not path.exists():
 path.write_text(secrets.token_urlsafe(32)+'\n'); path.chmod(0o644)
PYSECRET
certs="${CONTROL_CERTS_DIR:-.artifacts/control-certs}"
mkdir -p "$certs"
if [ -f "$certs/controller.key" ] && [ -f "$certs/agent.key" ] && openssl x509 -checkend 3600 -noout -in "$certs/agent.crt" >/dev/null 2>&1 && openssl x509 -checkend 3600 -noout -in "$certs/controller.crt" >/dev/null 2>&1; then exit 0; fi
umask 077
openssl req -x509 -newkey rsa:2048 -nodes -keyout "$certs/ca.key" -out "$certs/ca.crt" -days 30 -subj /CN=streamtool-local-ca >/dev/null 2>&1
for name in agent controller; do
 openssl req -newkey rsa:2048 -nodes -keyout "$certs/$name.key" -out "$certs/$name.csr" -subj "/CN=streamtool-$name" >/dev/null 2>&1
 if [ "$name" = agent ]; then
  printf 'subjectAltName=DNS:agent\nextendedKeyUsage=serverAuth\n' > "$certs/extensions"
 else
  printf 'extendedKeyUsage=clientAuth\n' > "$certs/extensions"
 fi
 openssl x509 -req -in "$certs/$name.csr" -CA "$certs/ca.crt" -CAkey "$certs/ca.key" -CAcreateserial -out "$certs/$name.crt" -days 30 -extfile "$certs/extensions" >/dev/null 2>&1
done
rm "$certs/extensions" "$certs/agent.csr" "$certs/controller.csr"
# Local development certificates must be readable by the non-root API container.
chmod 644 "$certs/ca.crt" "$certs/controller.crt" "$certs/controller.key" "$certs/agent.crt" "$certs/agent.key"
