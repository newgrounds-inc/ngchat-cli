# Auth: login once, keep the NG cookie, re-mint hourly via GET jwt.php

Amended by ADR 0003: `jwt.php` is gone, the re-mint goes through
`POST /api/v1/auth/service-token` with a per-run cookie jar, and the
interim password and `set-cookie` paths are removed. The decision here
(store the remember cookie, never the password) stands.

The chat JWT lives 1 hour and the chat server hard-closes sockets when it
lapses, so a CLI needs a durable credential to re-mint from. We store the
site's remember cookie and re-mint through the existing cookie-authed
`GET /ngapps/jwt.php` — which already works for any logged-in NG account —
rather than opening the password-grant `POST jwt.php` to all users.

## Considered Options

- **Stateless password+2FA grant on POST jwt.php** — rejected. The site's
  2FA machinery is session-bound (challenge state lives in the Laravel
  session), the endpoint's per-IP rate limiting is broken (synthetic
  requests make every caller `127.0.0.1`), its failure responses are not
  JSend, and its pipeline skips retirement checks and login-event
  recording. Fixing all of that amounts to designing a new stateless auth
  protocol for one client.
- **Store the account password locally** — rejected outright; the cookie
  is revocable server-side, the password is not.

## Consequences

- The site still needs a small amount of new work: JSON-shaped login and
  2FA-submit endpoints for the CLI's first login (designed and reviewed in
  the site repo, not here), plus the rate-limiter IP fix.
- Until that ships, password login works only for `ngapps.allowed_users`
  bot accounts, and other users can stash a browser-obtained cookie via
  `ngchat set-cookie`.
- The remember cookie's `users_tokens` row also suppresses the site's
  new-IP email challenge for 30 days, which keeps re-mints unattended.
