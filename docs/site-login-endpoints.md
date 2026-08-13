# Site-side login endpoints for the CLI — working brief

Input for the future design session in the main site repo
(`newgrounds-site`). Captures the August 2026 recon of the site's auth
code so that session doesn't start from scratch. File references are to
`newgrounds-site` at that time. See ADR 0001 for the client-side decision
this serves.

## What the CLI needs

1. **JSON login endpoint** running the *full* login pipeline (throttle →
   2FA check → authenticate → session prep → retirement restore → login
   event → cleanup), returning JSend and setting the session/remember
   cookies. The CLI stores the remember cookie and never the password.
2. **JSON 2FA-submit endpoint** to complete an email-code or TOTP
   challenge started by (1).
3. **JSend shapes for every outcome**: success, 2FA-required
   (intermediate state), bad credentials, lockout. Today's failure paths
   leak Laravel's `{message, errors}` shape (422) and `LockoutResponse`
   (429) instead of JSend.
4. After first login the CLI only uses cookie-authed `GET
   /ngapps/jwt.php` for hourly re-mints — that path already works for
   any logged-in account and needs no changes.

## Why `POST jwt.php` can't just be opened up (recon findings)

- **2FA is session-bound.** `JwtManager::handlePost()`
  (`ngapps/jwt_manager.php:142-179`) builds a synthetic
  `LoginRequest::create()` with no session, no cookies, no client IP.
  `RedirectIfTwoFactorRequiredAction` writes challenge state to
  `$request->session()` (`:80-86`), and `TwoFactorLoginRequest::authorize()`
  + `TwoFactorRateLimiter` both require session keys. Dropping the action
  into the ngapps pipeline as-is throws.
- **Per-IP rate limiting on POST is effectively absent.** The Predis IP
  limiter in `JwtManager` is GET-only (`WebController::token` returns
  before `limitAttemptRate()`), and because of the synthetic request,
  `LoginRateLimiter`'s IP keys all see `127.0.0.1` — the whole site
  shares one 100-failures/15-min bucket and one 3/60s per-username
  bucket. Must be fixed before any credential endpoint is exposed
  broadly (and the fix is worth shipping regardless).
- **No ngapps response contracts exist.**
  `NeedsTwoFactorAuthenticationResponseContract` /
  `UndeliverableTwoFactorCodeResponseContract` are bound only for the
  Account and Passport surfaces (`AuthServiceProvider:107-160`); that's
  precisely why `Ngapps\LoginController` skips the 2FA action. Two new
  response classes + bindings and a 2FA-submit route are needed.
- **The ngapps pipeline skips post-login steps** that become user-visible
  once real users log in here: no session regeneration, retired accounts
  authenticate without un-retiring, no `Login` user event recorded, no
  stale login-code cleanup.
- **`login_type` "Email only" isn't enforced on this route**
  (`Ngapps\LoginController:38-46`, tracked as site issue #3848) — the
  check must move into `AttemptToAuthenticateAction` or the UserProvider
  before real users use it.
- **`LoginRequest::authorize()` is `auth()->guest()`** — a caller with a
  valid session cookie gets a 403. A CLI that keeps cookies to survive
  2FA will trip this unless the endpoint tolerates it.
- **jwt.php POST already mints a full site session + `users_tokens` row**
  per call. Hourly password re-POSTs would accumulate session rows
  (pruned after 1 day); the cookie-jar flow avoids this entirely.

## Pre-existing 2FA inconsistencies to settle first

- `AuthType::EMAIL_QR` (email + authenticator) is missing from
  `AUTHENTICATOR_TYPES`, so those users are routed down the email-code
  branch and never reach TOTP.
- `needsAuthenticatorApp()` ignores `UserQrCode.active` (legacy code
  checks it).
- TOTP verification has no replay protection or drift window; TOTP
  secrets and backup codes are stored in plaintext (acknowledged in
  code comments). Worth resolving before TOTP gates a scriptable
  endpoint.
- The new-IP email challenge (`needsEmailCode()`) is skipped when a
  `users_tokens` row matches user+IP within 30 days — the remember row
  the CLI stores keeps unattended re-mints alive; `remember=false`
  sessions are pruned after ~1 day and would re-challenge nearly every
  run.

## Placement note

`POST /api/v1/auth/service-token` already lets any *session-authed* user
mint a chat JWT (no allowlist), with JSend and proper limiters — but it
sits behind `auth` + CSRF, and `docs/API-Design.md:52-66` says credential
grants belong to the (unbuilt) Sanctum PAT design. Whether the CLI login
lands under `/api/v1` or `ngapps` is the first call for the site session.
