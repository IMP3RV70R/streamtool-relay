# Self-host HTTPS proxy

This is standard Caddy 2.11.4 with its standard modules. A small entrypoint and
checked-in go.mod/go.sum pin its transitive security updates; the Dockerfile builds
with Go 1.26.8, replaces the stock binary and upgrades Alpine runtime packages.
Packaging and image security checks both build this image; deployment executes its
immutable image ID rather than a moving upstream tag.

Review updates with `go mod tidy`, the security-source build, container scanning and
Caddyfile validation. Keep the stable Caddy-compatible CEL version: CEL 0.30 changes
an API used by Caddy 2.11.4 and cannot be substituted without upstream adaptation.
Govulncheck reports no reachable vulnerable symbols for this entrypoint, but reports
a package-level CEL finding and the unmaintained OpenPGP module finding; these are
not blanket exemptions. The final HIGH/CRITICAL image scan passed locally.

Actual public TLS issuance/renewal and host acceptance remain publication gates.
