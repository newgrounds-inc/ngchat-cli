# Site-side login endpoints for the CLI

Two parts. **Contract** is what the CLI is built against
(`httptest` fakes here, real endpoints in `newgrounds-site`). **Recon**
below it is the August 2026 survey of the site's auth code that the
contract was designed around. See ADR 0001 and
`docs/plans/mvp-release.md` for the client-side decisions.

## Contract (agreed 2026-09-02)

All three new routes live under `/api/v1/auth/`, inside the existing
`api/v1` group (JSend, `EnforceJsonContentNegotiation`, CSRF from the
`web` group). Responses follow the site's `JsendResponse` envelope:
`{"status":"success","data":{...}}`,
`{"status":"fail","data":{...}}`,
`{"status":"error","message":"...","code":...}`.

The CLI keeps an in-memory cookie jar for the run. Only the
`ng_remember` value is persisted (OS keyring, `0600` file fallback).

### `GET /api/v1/auth/csrf`

Primes the session and sets `XSRF-TOKEN`. Returns `204 No Content`.
Guest-callable. Rate-limited under the general `api` limiter. The CLI
calls it once before `login` and once per run before the first
`service-token`; the jar carries the cookies after that. If a later POST
returns 419 the CLI calls it again and retries once.

### `POST /api/v1/auth/login`

Runs the **full** login pipeline (throttle → 2FA check → authenticate →
session prep → retirement restore → login event → cleanup). Requires
`X-XSRF-TOKEN` (the `XSRF-TOKEN` cookie, URL-decoded).

Request:

```json
{"identifier": "username-or-email", "password": "...", "remember": true}
```

Outcomes:

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `{"two_factor": null, "user": {"user_id", "username"}}` | logged in; store `ng_remember` from `Set-Cookie` |
| 200 | success | `{"two_factor": "email"}` | prompt for the emailed code |
| 200 | success | `{"two_factor": "totp"}` | prompt for the authenticator code |
| 422 | fail | `{"credentials": "..."}` | show message, re-prompt (password step) |
| 422 | fail | `{"login_type": "email_only"}` | show "this account has no password login", exit |
| 429 | fail | `{"lockout": "...", "retry_after": <seconds>}` | show message with wait time, exit |
| 403 | fail | `{"session": "already authenticated"}` | should not happen (fresh jar); clear jar and retry once |

`remember=true` is carried across the 2FA step by the site (session key
`login.remember`), so the CLI does not resend it. The `two_factor` state
is session-bound: the CLI must send the same jar to `two-factor`.

The success response sets `ng_remember` (400-day cookie). The CLI reads
it from `Set-Cookie` and persists only that value.

### `POST /api/v1/auth/two-factor`

Completes a challenge started by `login`. Same jar, same CSRF header.

Request:

```json
{"code": "123456"}
```

Outcomes:

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `{"user": {"user_id", "username"}}` | logged in; store `ng_remember` |
| 422 | fail | `{"code": "..."}` | re-prompt; after 3 failures restart from `login` |
| 410 | fail | `{"challenge": "expired"}` | restart from `login` |
| 503 | fail | `{"email": "undeliverable"}` | show the site's message verbatim, exit |
| 429 | fail | `{"lockout": "...", "retry_after": <seconds>}` | show message with wait time, exit |

No resend endpoint in the MVP.

### `POST /api/v1/auth/service-token` (exists today)

Used hourly for re-mint. Request `{"service": "chat"}` with the jar and
`X-XSRF-TOKEN`. Success `data` is `{"service","token","expires_in"}`.

- `401` means the remember token is gone (logout elsewhere is not
  possible today, so: password changed, or the CLI logged out). The
  CLI treats it as **signed out**: stop the client, exit the TUI, print
  `run ngchat login`. Never retry.
- `419` means CSRF mismatch: re-prime with `csrf`, retry once.
- The `service_token` limiter (10/min per user or IP) counts **before**
  auth and CSRF, so any CLI loop also throttles the user's browser. The
  CLI mints exactly once per server `revalidate` and once per connect.

### Site-side notes the contract depends on

- Pre-existing 2FA inconsistencies (below) can ship independently; the
  CLI just follows whichever challenge the site returns.
- The `LoginRateLimiter` IP-key bug (every caller sees `127.0.0.1`
  through the synthetic request) must be fixed before `login` is
  exposed. The new endpoint uses the real request, so the fix is to
  stop using `LoginRequest::create()` synthetically, not to touch the
  CLI.
- No user-facing session or device revocation exists today. Worth a
  follow-up on the site, not required for the CLI.

---

## Recon (August 2026)

### What the CLI needs

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

### Why `POST jwt.php` can't just be opened up (recon findings)

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

### Pre-existing 2FA inconsistencies to settle first

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

### Placement note

`POST /api/v1/auth/service-token` already lets any *session-authed* user
mint a chat JWT (no allowlist), with JSend and proper limiters — but it
sits behind `auth` + CSRF, and `docs/API-Design.md:52-66` says credential
grants belong to the (unbuilt) Sanctum PAT design. Whether the CLI login
lands under `/api/v1` or `ngapps` is the first call for the site session.
