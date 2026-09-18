# API contracts

This directory is the home for the website/backend boundary: OpenAPI schemas,
wire-format examples and versioning notes. Android is resumed and adapts directly to
the current contract. No OpenAPI specification or generated client is implemented yet.

Until the public API is formalized, the implemented routes are in
[`backend/internal/application/api.go`](../backend/internal/application/api.go) and
the public owner contract is documented in [`user-api.md`](user-api.md).

When changing the application API:

1. Describe authentication, request/response schemas and error semantics here.
2. Change backend handlers and contract validation together.
3. Keep generated platform-specific clients in their consuming app.
4. While lifecycle status is `PRE-LAUNCH`, move all in-scope consumers directly to
   the clean contract. After status becomes `PUBLISHED`, version incompatible changes
   so released clients can coexist with the backend during upgrades.

Do not expose database models or internal Agent protocols as client contracts.

The authenticated same-origin source API includes `GET/PUT/DELETE /v1/me/source/fallback`.
PUT is binary `image/png`, `image/jpeg` or `video/mp4`, capped at 50 MiB, with
`X-Streamtool: 1` and `?generation=N`. GET exposes kind, size and generation only,
never media content; absent returns null. DELETE keeps a durable default tombstone
and advances generation. Both mutations require no active session or publisher.
An MP4 must contain one H.264 video up to 30 seconds and 1080p; audio is discarded.
The isolated worker decodes images/videos directly and loops video with silence.
