# Site login endpoints the CLI uses

The contract `internal/auth` is built against: `fakeSite` in
`site_test.go` models it, and the real routes live on the site under
`/api/v1/auth/`. See ADR 0001 for why a cookie jar, ADR 0003 for the
re-mint path, and ADR 0004 for how the stored cookie is keyed.

Responses use the site's JSend envelope:
`{"status":"success","data":{...}}`,
`{"status":"fail","data":{...}}`,
`{"status":"error","message":"...","code":...}`.

In every `fail` body, `data` is a map of field name to a **list** of
message strings, e.g.
`{"identity":["These credentials do not match our records."]}`. The CLI
prints the first message of whichever key it recognises and falls back
to the first message of any key.

The CLI keeps an in-memory cookie jar for the run. Only the
`ng_remember` value is persisted (OS keyring, `0600` file fallback).

## Request rules that apply to every call

- Send `Accept: application/json` and, on POST, `Content-Type:
  application/json` with a JSON body. The group refuses a non-JSON
  Accept with 406 and a non-JSON POST body with 415, both as JSend
  `fail`.
- Send `X-XSRF-TOKEN` on every POST: the current `XSRF-TOKEN` cookie
  value from the jar, URL-decoded. **Read it from the jar immediately
  before each POST**, never cache it: the site rotates the token when a
  login step completes (`login` success and `two-factor` success both
  regenerate the session), so the value that worked for `login` is
  stale by the time `service-token` is called.
- `419` on any POST means CSRF mismatch: re-prime (below) and retry once.

## Priming the jar (no dedicated endpoint)

There is no `csrf` route. A guest `GET /api/v1/auth/me` answers
`401` JSend `fail` `{"auth":["Unauthenticated."]}` **and** sets both the
`XSRF-TOKEN` and session cookies, which is all the CLI needs. The CLI
calls it once before `login` and once per run before the first
`service-token`. It costs one hit of the guest API limiter (30/min per
IP).

## `POST /api/v1/auth/login`

Runs the full site login pipeline (throttle, credential check, 2FA
challenge, authenticate, session regeneration). Guest-only: an
already-authenticated jar gets 403.

Request:

```json
{"identity": "username-or-email", "password": "...", "remember": true}
```

`remember` is a boolean; the CLI always sends `true`.

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

`remember=true` is carried across the 2FA step by the site, so the CLI
does not resend it. The `two_factor` state is session-bound: the CLI
must send the same jar to `two-factor`.

The success response sets `ng_remember` (400-day cookie). The CLI reads
it from `Set-Cookie` and persists only that value.

## `POST /api/v1/auth/two-factor`

Completes a challenge started by `login`. Same jar, same CSRF rules.

Request, one of:

```json
{"code": "123456"}
{"recovery_code": "xxxx-xxxx"}
```

`code` is exactly 6 characters (the emailed code or the TOTP code).
`recovery_code` is a TOTP backup code, only meaningful when `login`
returned `"totp"`; each one is single-use. The CLI has one code prompt:
a six-character entry goes as `code`, anything else as `recovery_code`.

Outcomes:

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `{"user": {"id", "username"}}` | logged in; store `ng_remember` |
| 422 | fail | `{"code": ["..."]}` or `{"recovery_code": ["..."]}` | re-prompt; after 3 failures restart from `login` |
| 403 | fail | `{"http": ["..."]}` | no challenge in this session (expired, consumed, or the jar was reset): restart from `login` |
| 429 | fail | `{"identity": ["Too many login attempts. ..."]}` | show message with the wait, exit |

No resend endpoint. An emailed code expires after 1 hour; after that
the site answers 422 and the CLI restarts from `login`.

## `POST /api/v1/auth/logout`

`ngchat logout`. Same jar, same CSRF rules; the jar is seeded with the
stored remember cookie and primed. The site revokes this device's
remember token and invalidates the session, so a copy of the cookie
kept anywhere else stops minting.

| HTTP | JSend | `data` | CLI action |
| --- | --- | --- | --- |
| 200 | success | `null` | revoked; clear the local copies, print `logged out` |
| 401 | fail | `{"auth": ["Unauthenticated."]}` | already a guest (password changed, logged out elsewhere): same as success |
| other / no answer | | | keep clearing locally, but exit 1 and say the cookie is still valid on the site |

## `POST /api/v1/auth/service-token`

Used on every connect and once per server `revalidate` nudge. Request
`{"service": "chat"}` with the jar and `X-XSRF-TOKEN`. Success `data`
is `{"service","token","expires_in"}`.

- `401` means the remember token is gone (password changed, or a
  logout ran, from the CLI or the site). The CLI treats it as
  **signed out**: stop the client, exit the TUI, print `run ngchat
  login`. Never retry.
- `419` means CSRF mismatch: re-prime, retry once.
- The limiter on this route (10/min per user or IP) counts **before**
  auth and CSRF, so any CLI loop also throttles the user's browser. The
  CLI mints exactly once per `revalidate` and once per connect.
