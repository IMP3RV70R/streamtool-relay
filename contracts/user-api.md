# User cabinet API

The website and native Android cabinet use this same contract. Public JSON uses
snake_case. Ownership is derived from the opaque `streamtool_session` cookie, never
from client-selected account IDs. Android accepts the cookie only after the same
password + second-factor login/confirmation and encrypts its value in Android
Keystore-backed storage bound to the validated server origin. No separate native
login/register routes or bearer-token authentication are supported.

The current API covers only account access, stream control, one source,
image/video fallback, up to eight outputs, optional output quality and first-owner setup. The authoritative endpoint table is in
[docs/CABINET.md](../docs/CABINET.md#основной-owner-api).

`POST /v1/me/source` idempotently provisions the always-available account source. Both clients do this
automatically after authentication. GET and POST return the encrypted-at-rest source key to its authenticated owner
with `Cache-Control: no-store`; repeat requests preserve the key, outputs and
active session. Historical hash-only sources return an empty key until the owner
explicitly rotates it; migration never silently replaces credentials. There is no source
disable/delete route. Enabled outputs receive media when a source session starts.

`GET /v1/me/source/status` includes `can_stop` and `stop_block_reason`:
`SOURCE_CONNECTED`, `SOURCE_STATE_UNKNOWN`, `SESSION_INACTIVE`, or an empty reason
when completion is permitted. `POST /v1/me/source/stop` requires a confirmed
publisher disconnection and a successful edge observation within 10 seconds;
otherwise HTTP 409. Missing frames alone never permit completion. A transient
source disconnect or graceful shutdown does not end the broadcast. With automatic
slate enabled it stays on air until source recovery or explicit completion; with
slate disabled it stays running as NO_SIGNAL. New future source connections start
normally without an explicit start action.

`GET` and `PUT /v1/me/source/slate` use `on_source_loss`, `forced` and `generation`.
Updates are applied to a running worker without restarting the output connection.

`GET /v1/me/source/outputs` returns an array of objects (`id`, `name`, `endpoint`,
`enabled`, `generation`), empty when absent. `POST` creates an output with generation
0 (HTTP 201, id). `PUT /v1/me/source/outputs/{id}` updates with the current generation
(204). Blank secrets retain existing keys; creation requires a key. Stale writes
return 409. `DELETE /v1/me/source/outputs/{id}` with generation archives the record
and erases its secret (204). `POST /v1/me/source/outputs/{id}/retry` with generation
retries an enabled output (204). Unknown or archived IDs return 404. SQLite enforces
at most eight non-archived outputs, including disabled ones, atomically for all writers.
Active names are unique per source. Each connection retries independently; all outputs
share one encoder, quality and fallback. Different quality per destination is outside scope.

`GET/PUT /v1/me/source/media` returns/sets `width`, `height`, `fps_num`, `fps_den`,
`video_kbps`, `audio_kbps`, `generation`. PUT requires a current generation and an
inactive session with disconnected publisher; 409 otherwise, invalid profiles 422.
Even dimensions up to 1920×1080 pixels, 15–60 FPS including rational rates,
video 100–8000 kbps, audio 64–320 kbps. Source and slate use the same H.264/AAC
output profile. The common encoder normalizes source resolution/FPS, controls CBR
and emits a two-second keyframe interval continuously across source/fallback changes.
Input and output profiles need not match. Defaults are 720p30/3000 kbps; no explicit
quality selection is required. Fallback images/videos are decoded directly without
profile preparation or an encoded cache. Standard Twitch recommends
up to 6000 kbps video; higher configuration is not provider acceptance.
Enhanced Broadcasting/HEVC/multiple tracks/1440p are deferred.

`GET/PUT/DELETE /v1/me/source/fallback`: GET returns null or `kind`, `bytes`,
`generation`; content is private. PUT sends binary PNG/JPEG/H.264 MP4 with
`X-Streamtool: 1` and `?generation=N`, not JSON. Maximum upload is 50 MiB;
MP4 has one 8-bit 4:2:0 H.264 Baseline/Main/High video up to 30 seconds/1080p. Uploaded audio is discarded.
Both mutations return 204, require no active session/publisher and compare the
current generation (409 on conflict). DELETE restores the built-in image with a
durable default tombstone and advanced generation. Invalid asset decoding fails the worker rather than silently substituting another asset.
Unsupported source codecs/limits follow the fallback policy; a supported source clears
its media error. Resolution/FPS differences within supported limits are normalized.

Owner mutations are limited to 60/minute/account in the single control process.
Rate/concurrency rejection returns HTTP 429 and `Retry-After` where available.

`GET /v1/auth/setup` returns `{required: boolean}`: true until the owner has TOTP.
`POST /v1/auth/setup` accepts `{password}` (12–256 bytes), `X-Streamtool: 1`
and the private `X-Setup-Token`. An existing owner without TOTP must supply their
current password. An owner with TOTP receives 409; the installation token cannot
reset an enabled authenticator or act as a login credential. Wrong token: 403.

Successful setup returns 200 with `{enrollment_token, secret, qr, recovery_codes,
expires_in: 600}` and **no session cookie**. The QR is a locally generated PNG data
URL. Ten recovery codes are shown only here; save them before confirmation.
A later authorized enrollment replaces the outstanding challenge.

`POST /v1/auth/setup/confirm` accepts `{enrollment_token, code}`. The unexpired
256-bit challenge and a valid six-digit new TOTP are required. This atomically
creates/preserves the owner, enables the encrypted authenticator, stores recovery
hashes, consumes the challenge and issues the seven-day HttpOnly/Secure/Strict
session. Wrong/expired challenge or code: 401 with no partial owner/session creation.
The setup token is not required again for this capability-protected confirmation.

`POST /v1/auth/login` accepts `{password, code}` **or** `{password, recovery_code}`.
Email is not accepted. Both factors must pass; missing/incorrect/replayed factors
return the same 401. A TOTP is six digits, SHA-1 HMAC, 30-second step with ±1-step
clock tolerance; a consumed or older step is rejected durably. Recovery codes are
128-bit random values and are consumed once, atomically with session creation.
Successful confirmation/login returns `{authenticated: true}`.

`POST /v1/auth/totp/replace` accepts the same password + second-factor body as login.
It consumes that proof and returns an enrollment without issuing a new session.
The current authenticator/sessions remain usable until setup/confirm succeeds;
confirmation replaces the authenticator/codes, revokes all existing owner sessions
and issues one new session. Installation tokens cannot authorize replacement.

`POST /v1/auth/logout` revokes the persisted session. `GET /v1/me` returns
`{account_id}` without email. Setup, confirmation, replacement and login share
persistent limits: 10 requests per IPv4 or IPv6 /64 per 15 minutes, 10 per owner
across addresses per 15 minutes, and 60 per installation per minute. HTTP 429
includes Retry-After. Invalid bodies and installation tokens count too.
See [cabinet security](../docs/CABINET_SECURITY.md) and [offline recovery](../docs/SELFHOST.md).

`/v1/edge/auth` does not exist on the public API (HTTP 404, including with an
operator token). MediaMTX calls the dedicated private listener. Admission there
uses the actual peer IP, not forwarded headers; production transport requires TLS.

Twitch OAuth, channel management, broadcast title, plural owner destination routes,
analytics, chat/community collection and history, moderation, Telegram and EventSub
webhook routes do not exist in the active contract.
