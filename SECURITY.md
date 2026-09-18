# Security policy

## Supported versions

The project is pre-launch and has no published supported releases. Development
builds and local verification are not a production security guarantee.

## Reporting a vulnerability

Do not disclose exploit details, credentials or private logs in a public issue.
No dedicated private reporting channel is currently configured. Request a private
contact from the maintainer without including vulnerability details in a public issue.

Include the affected revision, deployment type, reproduction steps, impact and a
sanitized example through the private channel. Never include stream keys, SSH
passwords, session cookies, TOTP secrets, recovery codes or signing seeds.

## Deployment boundaries

Use the self-host installer with independent generated credentials. Values under
`infra/local/secrets` are public development fixtures, not production secrets.
Keep SSH, Docker access, private keys and backups restricted to the host administrator.
Verify release signatures using the independently pinned public key; a download
checksum alone does not establish publisher authenticity.

See [cabinet authentication](docs/CABINET_SECURITY.md),
[deployment requirements](docs/PRODUCTION.md) and
[media dependency review](docs/MEDIA_SECURITY.md) for implemented controls and
current verification limits. There has been no independent security audit.
