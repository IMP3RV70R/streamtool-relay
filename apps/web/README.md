# streamtool-relay website

Same-origin cabinet at `/dashboard`: source-loss controls, source credentials,
optional output quality, image/video fallback and up to eight independent RTMP/RTMPS outputs.
First setup requires a private installation code, a password and TOTP confirmation.
Later visits require the password plus TOTP or a single-use recovery code. Local QR,
manual enrollment, ten recovery codes and proof-required authenticator replacement
are included. There is no email, balance, billing, OAuth or public registration.

Visiting the page never provisions media. Explicit enable creates the source once;
source keys are issued only once and output keys are write-only. Output quality
has usable defaults; the encoder normalizes source size/FPS automatically.
Fallback is PNG/JPEG or H.264 MP4 up to 30 seconds/50 MiB/1080p, decoded directly
with silence. Asset/quality changes require publisher disconnection and no session.

Tests in `tests/web` cover browser contracts; SQLite and real-media tests separately
verify authority and actual behavior. Android uses the same owner API; real Twitch acceptance remains outstanding.
See [self-host guide](../../docs/SELFHOST.md) and [API contract](../../contracts/user-api.md).
