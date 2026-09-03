# Site-side login endpoints for the CLI

Two parts. **Contract** is what the CLI is built against
(`httptest` fakes here, real endpoints in `newgrounds-site`). **Recon**
below it is the August 2026 survey of the site's auth code, kept for
history, with a corrections block on top from the 2026-09-02 site
session. See ADR 0001 and `docs/plans/mvp-release.md` for the
client-side decisions. The site-side implementation plan is
`newgrounds-site/docs/plans/api-v1-auth-login.md`.

## Contract (agreed 2026-09-02, revised the same day after the site review)

Two new routes, not three (plus the logout route that landed with
GH #4008), under `/api/v1/auth/` inside the existing `api/v1` group (JSend, `EnforceJsonContentNegotiation`, CSRF from the
`web` group). Responses use the site's `JsendResponse` envelope:
`{"status":"success","data":{...}}`,
`{"status":"fail","data":{...}}`,
`{"status":"error","message":"...","code":...}`.

In every `fail` body, `data` is a map of field name to a **list** of
message strings (Laravel validation shape), e.g.
`{"identity":["These credentials do not match our records."]}`. The CLI
prints the first message of whichever key it recognises and falls back
to the first message of any key.

The CLI keeps an in-memory cookie jar for the run. Only the
`ng_remember` value is persisted (OS keyring, `0600` file fallback).

### Request rules that apply to every call

- Send `Accept: application/json` and, on POST, `Content-Type:
  application/json` with a JSON body. The group refuses a non-JSON
  Accept with 406 and a non-JSON POST body with 415, both as JSend
  `fail`.
- Send `X-XSRF-TOKEN` on every POST: the current `XSRF-TOKEN` cookie
  value from the jar, URL-decoded. **Read it from the jar immediately
  before each POST**, never cache it: the site rotates the token when a
  login completes (`login` success and `two-factor` success both
  regenerate the session), so the value that worked for `login` is
  stale by the time `service-token` is called.
- `419` on any POST means CSRF mismatch: re-prime (below) and retry once.

### Priming the jar (no dedicated endpoint)

There is no `csrf` route. A guest `GET /api/v1/auth/me` answers
`401` JSend `fail` `{"auth":["Unauthenticated."]}` **and** sets both the
`XSRF-TOKEN` and session cookies, which is all the CLI needs. Verified on
dev 2026-09-02. The CLI calls it once before `login` and once per run
before the first `service-token`. Costs one hit of the guest `api`
limiter (30/min per IP).

### `POST /api/v1/auth/login`

Runs the **full** site login pipeline (the same one the account and
passport pages use: throttle, credential check, 2FA challenge,
authenticate, session regeneration, retirement restore, login event,
cleanup). Guest-only: an already-authenticated jar gets 403.

Request:

```json
{"identity": "username-or-email", "password": "...", "remember": true}
```

`identity` is the site's field name (the shared `LoginRequest` is
final, so the API reuses it as-is). `remember` is a boolean; the CLI
always sends `true`.

Outcomes:

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `{"two_factor": null, "user": {"id", "username"}}` | logged in; store `ng_remember` from `Set-Cookie` |
| 200 | success | `{"two_factor": "email", "obfuscated_email": "b***@example.com"}` | prompt for the emailed code (valid 1 hour) |
| 200 | success | `{"two_factor": "totp", "obfuscated_email": null}` | prompt for the authenticator code, or a recovery code |
| 422 | fail | `{"identity": ["..."]}` or `{"password": ["..."]}` | show message, re-prompt from the password step (an empty password there is not sent; it returns to the identity prompt) |
| 422 | fail | `{"undeliverable": ["..."]}` | show the message verbatim, exit; the account's email cannot receive a code and only support can fix it |
| 429 | fail | `{"identity": ["Too many login attempts. Please try again in ..."]}` | show message verbatim, exit. No `retry_after` field; the human-readable wait is inside the message |
| 403 | fail | `{"http": ["..."]}` | jar is already authenticated; should not happen with a fresh jar. Clear jar, re-prime, retry once |

Not distinguishable, by design: an account set to "Login with: Email
only" that submits its **username** gets the same 422 as a wrong
password. The site deliberately gives no distinguishing response so the
preference cannot be probed. The CLI cannot show a specific "no password
login" message; instead the login help text says to enter the email
address if the account uses email-only login.

`remember=true` is carried across the 2FA step by the site (session key
`login.remember`), so the CLI does not resend it. The `two_factor` state
is session-bound: the CLI must send the same jar to `two-factor`.

The success response sets `ng_remember` (400-day cookie). The CLI reads
it from `Set-Cookie` and persists only that value.

### `POST /api/v1/auth/two-factor`

Completes a challenge started by `login`. Same jar, same CSRF rules.

Request, one of:

```json
{"code": "123456"}
{"recovery_code": "xxxx-xxxx"}
```

`code` is exactly 6 characters (the emailed code or the TOTP code).
`recovery_code` is a TOTP backup code, only meaningful when `login`
returned `"totp"`; each one is single-use. The MVP CLI prompts for
`code` and accepts `/recovery <code>` (or similar) to send a
`recovery_code` instead.

Outcomes:

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `{"user": {"id", "username"}}` | logged in; store `ng_remember` |
| 422 | fail | `{"code": ["..."]}` or `{"recovery_code": ["..."]}` | re-prompt; after 3 failures restart from `login` |
| 403 | fail | `{"http": ["..."]}` | no challenge in this session (expired, consumed, or the jar was reset): restart from `login` |
| 429 | fail | `{"identity": ["Too many login attempts. ..."]}` | show message with the wait, exit |

No resend endpoint in the MVP. An emailed code expires after 1 hour;
after that the site answers 422 and the CLI restarts from `login`.

### `POST /api/v1/auth/logout` (site GH #4008)

`ngchat logout`. Same jar, same CSRF rules; the jar is seeded with the
stored remember cookie and primed. The guard deletes this device's
`users_tokens` rows and invalidates the session, so a copy of the
cookie kept anywhere else stops minting.

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `null` | revoked; clear the local copies, print `logged out` |
| 401 | fail | `{"auth": ["Unauthenticated."]}` | already a guest (password changed, logged out elsewhere): same as success |
| other / no answer | | | keep clearing locally, but exit 1 and say the cookie is still valid on the site |

### `POST /api/v1/auth/service-token` (exists today)

Used hourly for re-mint. Request `{"service": "chat"}` with the jar and
`X-XSRF-TOKEN`. Success `data` is `{"service","token","expires_in"}`.

- `401` means the remember token is gone (password changed, or a
  logout ran, from the CLI or the site). The
  CLI treats it as **signed out**: stop the client, exit the TUI, print
  `run ngchat login`. Never retry.
- `419` means CSRF mismatch: re-prime, retry once.
- The `service_token` limiter (10/min per user or IP) counts **before**
  auth and CSRF, so any CLI loop also throttles the user's browser. The
  CLI mints exactly once per server `revalidate` and once per connect.

### Site-side notes the contract depends on

- Nothing in the site's 2FA code has to change first. The pre-existing
  inconsistencies listed in the recon are real but independent; the CLI
  follows whichever challenge the site returns.
- The two "must fix before exposing login" items from the original
  contract were wrong and are withdrawn; see the corrections block
  below.
- Device revocation is `POST /api/v1/auth/logout` (GH #4008, above);
  a user-facing list of devices does not exist yet and is not required
  for the CLI.

---

## Corrections to the recon (2026-09-02 site session)

Read these before trusting anything in the recon below.

1. **The site already has a JSON login + 2FA surface.** The passport
   login widget's `POST /passport/` and `POST /passport/two-factor`
   answer JSON when asked (`Accept: application/json` + `X-XSRF-TOKEN`),
   run the full pipeline, and are covered by
   `tests/Feature/Login/PassportTest.php`. Verified live on dev with a
   fresh non-browser cookie jar. The recon's "the site has no JSON
   login" was wrong; the MVP plan's version of that line is wrong too.
   The `/api/v1` pair was still chosen because passport's JSON is an
   undocumented contract for its own Alpine widget (Laravel's
   `{message, errors}` shape, `redirect` semantics, 204 on 2FA success)
   and can change without notice, while `/api/v1` is the documented,
   OpenAPI-covered home. Until the pair ships, the passport routes are a
   usable stand-in for end-to-end smoke tests.
2. **The `LoginRateLimiter` IP-key bug is not a blocker.** It exists only
   because `JwtManager::handlePost()` builds a synthetic `LoginRequest`
   with no client IP. Any real route keys the limiter on the real
   `$request->ip()`. The new endpoint is a real route, so nothing needs
   fixing before it ships. The bug still applies to `POST /ngapps/jwt.php`
   and goes away when that route is retired.
3. **The `login_type` "Email only" gap is Ngapps-only.** The check lives
   in `RedirectIfTwoFactorRequiredAction`, which every surface that runs
   the 2FA step (account, passport, and the new API pair) goes through.
   Site issue #3848 is about the ngapps pipeline skipping that action.
4. **No `csrf` endpoint is needed.** See "Priming the jar" above.
5. **The `LoginRequest` guest-only `authorize()`** stands; the contract
   handles the resulting 403.
6. **The `service-token` endpoint is the only shared surface** with NG
   Chat and NG Radio. The login pair is a login surface (session
   establishment), a different category from the SSO-handoff mint, and
   the site already has three of those (account, passport, ngapps). The
   new pair is the fourth, on the same pipeline; it does not add an auth
   mechanism.

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
