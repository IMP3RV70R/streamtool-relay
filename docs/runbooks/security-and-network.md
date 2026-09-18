# Security and network policy

## API and admission

Expose the website only through the HTTPS reverse proxy/load balancer. SQLite,
API backend, edge authorization, MediaMTX management and Agent stay private.
Production API startup requires TLS, secure cookies, trusted proxy CIDRs and a
private SQLite database with an installation-owned file key manifest.
See [cabinet authentication limits](../CABINET_SECURITY.md) for brute-force,
setup-token and session protection. Configure `TRUSTED_PROXY_CIDRS` with exact proxy addresses. The proxy must replace
or append the actual client address to X-Forwarded-For; never forward a client
header verbatim. Missing/malformed trusted-proxy headers fail closed.

MediaMTX calls `EDGE_AUTH_ADDR`, a distinct private TLS listener admitting only
`EDGE_AUTH_PEER_CIDRS` from the socket peer. Never proxy `/v1/edge/auth` through
the public frontend. Configure its HTTPS authHTTPAddress and a trusted certificate
(or authHTTPFingerprint); configure HTTPS for its management API as well.
API-to-edge requests verify certificates and reject redirects. Private listener
CIDRs are not a substitute for cloud security groups or network isolation.

Local Compose is explicitly `STREAMTOOL_ENV=development`. Its edge authorization
bridge contains only API and edge; it is not published. Local receiver exceptions
require both development mode and deployment AllowHosts, and still resolve DNS.
Production never bypasses IP checks for AllowHosts.

## Worker network boundary

Each native RTMP branch connects to a loopback relay. At EVERY TCP connection,
the relay checks all exact DNS dial candidates and dials an IP literal. RTMPS
upstream TLS verifies the original hostname; native librtmp sees only loopback
RTMP. Private, link-local/metadata, multicast, mapped IPv4 and reserved ranges are
denied outside explicit development fixtures. Destination keys are URL-escaped.

On Linux media VMs install root-owned `infra/vm/streamtool-worker-policy` at
`/usr/local/libexec/streamtool-worker-policy` (0755) and the root-owned
`worker-network.env` (0600), specifying a fixed IPv4 source edge and one dedicated
Docker worker network. Install/validate the narrow sudoers entry with visudo.
Required host tools: Docker CLI, bash, nsenter, iptables/ip6tables, tc, conntrack
kernel support. Agent runs on the VM, not inside a production Docker container.

The helper verifies container name/label/network/PID, enters only that worker's
network namespace and installs default-deny INPUT/OUTPUT before activation.
The only private outbound exception is source UDP on the exact configured port;
host gateway can reach authenticated control TCP 9090. Worker peers cannot reach
each other, cloud metadata or other management services. Public TCP is allowed
for generic output ports; loopback carries the relay. tc enforces allocation
egress shaping and ingress drop policing. CPU/RAM and PID limits remain enforced.
Policy installation failure stops the new container. Destination changes reapply
the bandwidth budget and persist its runtime snapshot in tmpfs for Agent restart.

The Agent delivers private files over Docker-exec stdin using the worker's closed
`runtime-io` commands. No caller-supplied paths are accepted. Archive members must
be bounded regular files with safe names; links, PAX metadata, duplicates and the
activation marker are rejected. All entries are staged before replacement, with
0600 permissions. Resources are validated and atomically renamed; activation
requires a valid resource snapshot and a readable regular control-token file.
Transport/activation failure stops the new worker. Token output is private to the
trusted Agent and must never be logged. GNU tar/touch/cat/mv and BusyBox are not
shipped; the managed startup barrier still uses dash/sleep.

Agent has Docker access and is therefore host-privileged by design; the sudo helper
does not make it an untrusted tenant. Its certificate, image allowlist, service
account and VM must stay outside workers. Node Agent systemd sandbox deliberately
permits this audited helper. Avoid Docker host networking/multiple worker networks.
Source addresses must match the host policy; update deployment config when changing
edges, not from user input. Network filtering and renewal require real VM acceptance.

## SQLite and key rotation

The API holds the private SQLite file lock and transactionally applies checksummed
migrations. Stop the API before offline migrate/backup/restore. Preserve the complete
key manifest and old key files/IDs; a new key must not rebind an existing ID. Never
rotate credentials to simplify migration. The backup includes database, keys,
certificate authority, installation settings and Agent high watermark.

The self-host installer supplies independent secrets, private TLS/mTLS and a daily
idle leaf-renewal timer. Host operations require Linux rehearsal; local development
certificates and network exceptions must not enter production.
See [self-host operations](../SELFHOST.md) and [production gates](../PRODUCTION.md).
