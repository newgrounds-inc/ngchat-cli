# Re-mint via `POST /api/v1/auth/service-token` with a per-run cookie jar

Amends ADR 0001. The site's SPA moved off `GET /ngapps/jwt.php` to
`POST /api/v1/auth/service-token`, and `jwt.php` is slated for deletion.
Interim password login was limited to allowlisted bots, and pasting a
browser cookie header was a dead end: the site session in it dies within
a day and the CSRF token the new endpoint requires rotates on every
login, so nothing pasted once can keep minting.

The CLI now logs in through the site's JSON login pair
(`POST /api/v1/auth/login`, `POST /api/v1/auth/two-factor`; contract in
`docs/site-login-endpoints.md`), keeps every cookie in an in-memory jar
for the run, and persists only the `ng_remember` value. Each run seeds
the jar with that value; a guest `GET /api/v1/auth/me` primes the session
and `XSRF-TOKEN` cookies; and every POST reads the CSRF token from the
jar immediately before sending, because the site rotates it when a login
step completes.

## Considered Options

- **Keep `GET jwt.php`** — rejected: it is being removed, and the CLI
  would have been the last caller keeping it alive.
- **Persist the whole cookie header** (v0.1's `set-cookie`) — rejected
  for the reasons above; the stored header cannot be turned into a
  remember value either, so migration deletes it and asks for one login.
- **Persist the account password** — rejected outright in ADR 0001; the
  remember cookie is revocable server-side (password change logs out
  every device), the password is not.
- **Inline re-login inside the TUI** — deferred. A 401 from
  `service-token` means the remember cookie is gone (password changed,
  or `ngchat logout` ran). The client stops with `ErrSignedOut`, the TUI
  exits, and main prints `run ngchat login`. Nothing else is retried.

## Consequences

- The keyring (or the `0600` fallback file) holds one value per site
  (ADR 0004): `ng_remember`, a 400-day cookie backed by a `users_tokens`
  row that expires after two years idle. Only `ngchat logout` or a
  password change invalidates it: logout calls
  `POST /api/v1/auth/logout` with the jar, which deletes the row, before
  clearing the local copies, and exits non-zero if the site did not
  answer, since a copy of the cookie would still mint until it does.
- Each run creates one fresh site session row (pruned after a day) and
  costs one hit of the guest `api` limiter (30/min per IP) to prime.
- The `service_token` limiter (10/min per user or IP) counts before auth
  and CSRF, so any client loop also throttles the user's browser. The
  client mints once per connect and once per server `revalidate`; a 419
  is re-primed and retried exactly once; a 401 is never retried.
- Cookie-based auth skips the login pipeline, so hourly re-mints never
  trigger the new-IP email challenge; the `users_tokens` row suppresses
  it for 30 days on a real login too.
- `NGCHAT_NG_COOKIE` (a raw cookie header seeded into the jar) stays as
  the dev and smoke override; `PasswordMinter`, `-user`, and
  `set-cookie` are gone.
