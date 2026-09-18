# Cabinet security

Updated: 2026-09-16. Implemented and locally tested; target-host acceptance is separate.

The self-host cabinet has one owner. Public registration does not exist. Initial
setup requires the installation's independently generated 256-bit setup token and
starts enrollment of the only owner. The owner and first session are committed only
after a valid TOTP confirmation. Keep that token and the deployment backup private.

## Login and setup admission

Every POST to login, setup, enrollment confirmation or authenticator replacement is counted before credentials are decoded or the setup
token is checked. Malformed bodies and wrong setup tokens consume the client/global
budget. A shared owner budget applies across all client addresses before decoding.

| Budget | Limit | Window |
| --- | --- | --- |
| Client IPv4 address or IPv6 /64 | 10 requests | 15 minutes |
| Owner, shared across client addresses and authentication routes | 10 requests | 15 minutes |
| Entire installation | 60 requests | 1 minute |
| Concurrent password-hash computations | 2 | Until computation completes |

These are fixed windows. Successful attempts also count. Exceeding a window returns
HTTP 429 with Retry-After (the window length); concurrent saturation returns 429
with Retry-After: 1. Counters are atomic and persist in SQLite across API restarts;
expired rows are collected in bounded batches. Database failure returns 503 and
never admits authentication. Credential bodies are limited to 4 KiB.

Only configured proxy addresses can supply X-Forwarded-For. The chain is parsed
from the nearest proxy backwards, with IPv4-mapped addresses normalized. A client
cannot change its budget merely by supplying another forwarded address. Configure
exact trusted proxy CIDRs; the API must remain inaccessible from the public network.

Passwords require 12–256 bytes and use salted PBKDF2-HMAC-SHA256 with 600,000
iterations. There is no email, username or mail delivery dependency. A missing owner still
performs a dummy password hash. Incorrect passwords and second factors return the
same generic 401 message, without claiming identical end-to-end timing.

## Authenticator and recovery

Login requires a password and exactly one second factor: a six-digit RFC 6238 TOTP
or an unused recovery code. TOTP uses a random 160-bit secret, HMAC-SHA1 and 30-second
steps, with one adjacent step accepted for clock drift. Synchronize server time.
The last consumed step is persisted; duplicate/older steps cannot log in again,
even concurrently or after restart. Wait for a fresh code after setup or login.

Enrollment returns a QR generated locally, a manual secret and ten random 128-bit
recovery codes. They are shown only during enrollment. The encrypted pending secret
and hashed challenge expire after ten minutes. Nothing grants a session before
confirmation. The enabled secret is encrypted with the installation's existing
versioned encryption keys and owner-bound associated data. SQLite stores recovery
code hashes only. Consumption and session creation commit in one transaction;
concurrent reuse can create at most one session.

Authenticator replacement requires the password plus a valid current TOTP/recovery
code. It consumes that proof, then starts a pending enrollment. The existing
second factor and browser sessions remain usable until confirmation. Confirmation
atomically activates the new secret/codes and revokes all older owner sessions and
recovery codes. A browser cookie alone cannot replace the authenticator.

Losing the phone can be handled with recovery codes; the password is still required.
One code can log in and another can authorize replacement. If all credentials are
lost, an administrator can stop the API and run the offline `api recover-owner`
command under the exclusive SQLite lock. This resets the password, revokes sessions,
second factor and pending enrollment, then requires installation token + new password
to enroll again. Media configuration, source/output keys and encryption keys remain
unchanged. There is no HTTP password-only recovery. See [the runbook](SELFHOST.md).

Migration 000005 revokes pre-MFA sessions without removing existing owners/passwords,
media or historical email metadata. Existing owners must enroll with their current
password and installation token; email is not accepted by the authentication API.
Backup restore revokes sessions, pending enrollment and recovery codes to avoid
resurrecting credentials consumed after the snapshot. It retains the authenticator
and advances the consumed counter beyond the current accepted TOTP window; wait up
to 60 seconds before logging in again.

## Sessions and browser requests

Session tokens contain 256 random bits. Only their SHA-256 digest is stored. Tokens
with an invalid shape are rejected before querying storage. Sessions expire after
seven days and logout revokes the stored session. Expired session records are
collected during successful login; active sessions are not deleted by cleanup.

Production cookies are Secure, HttpOnly and SameSite=Strict. Mutations require the
custom X-Streamtool header. An Origin, when present, must match the cabinet's origin;
cross-site browser requests are rejected. API responses use Cache-Control: no-store
and X-Content-Type-Options: nosniff. HTTPS terminates at Caddy with HSTS;
Caddy verifies the private API certificate and hides operator endpoints. Plain HTTP
and insecure cookies are development exceptions only.

## Boundaries

An attacker targeting the owner or using the same network can temporarily
exhaust a login budget. Existing authenticated sessions and running media continue;
the limit expires automatically and is not a permanent account lockout.

These controls are not a distributed DDoS shield. The host/proxy/provider must
handle connection floods and bandwidth exhaustion. TOTP is not phishing-resistant; passkeys and breached-password checks are not
implemented. A stolen authenticated browser session is a separate threat;
TOTP and rate limits do not prevent its use before revocation.

Worker isolation, network filtering, write-only output keys and image scan gates are
described in [the security runbook](runbooks/security-and-network.md).

## Verification

Application tests cover concurrent atomic admission, persistence across Handler
restart, IPv6 /64 aggregation, forged forwarding headers, cross-address owner limits,
global admission and expiry/cleanup, wrong setup tokens, oversized credentials and
foreign origins. RFC test vectors, concurrent TOTP/recovery replay, persistence,
encrypted seed storage, setup/confirmation, proof-required replacement, old-session
revocation, pre-MFA migration and offline recovery are covered locally. Chrome
checks cover setup, failed confirmation, recovery login and replacement. Real local eight-output media/recovery evidence and its limits are recorded in
[current status](STATUS.md). These tests do not establish public deployment or
provider acceptance.
