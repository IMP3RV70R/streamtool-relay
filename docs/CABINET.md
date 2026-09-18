# Browser cabinet

The same-origin `/dashboard` controls one SRT/RTMP source and up to eight independent
RTMP/RTMPS outputs. Twitch ingest URL and stream key are entered manually; no OAuth
or platform metadata integration.

## First setup and login

First setup requires the private installation token, chosen password, saving ten
recovery codes and confirming a new TOTP. Later login requires password plus TOTP
or an unused recovery code. Email and public registration are absent.
See [authentication protection](CABINET_SECURITY.md).

## Stream controls

- The source is provisioned automatically after login; routing is always available.
- Save source credentials when first issued. Rotation requires password confirmation.
- Configure outputs, toggle them, retry independently or remove them. Disabled
  outputs still occupy a slot; all outputs share one quality and fallback.
- Use the default 720p30/3000 kbps or change quality before streaming. Source size/FPS
  is normalized automatically.
- Select PNG/JPEG or a silent looped H.264 MP4 as fallback while inactive.
- Observe source/output state and choose automatic or forced fallback.
- Finish the broadcast only after confirmed publisher disconnection.

Source loss keeps fallback/silence on the current connection; return restores the
source in the same session. Whole-server failure requires recovery/new connections.
Stale observations cannot authorize stop. See [product behavior](PRODUCT_ALIGNMENT.md)
for media limits and [owner API](../contracts/user-api.md) for route/JSON semantics.

## Android and updates

The native [Android cabinet](../apps/android/README.md) uses the same owner API and
origin-bound cookie semantics. Its [SSH installation protocol](ONBOARDING.md) is
implemented but disabled without signed distribution configuration. Graphical
update controls and a browser installer are absent.
