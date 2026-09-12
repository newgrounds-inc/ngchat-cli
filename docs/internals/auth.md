# `internal/auth` — credentials, site login, chat JWTs

Read with `docs/site-login-endpoints.md` (the site contract this
package and its `httptest` fakes follow) and ADR 0001, 0003, 0004.

## `Site`: the cookie jar and the CSRF dance

`Site` wraps the site's `/api/v1/auth` routes with one in-memory cookie
jar per run: `Login` and `TwoFactor` for the first run, `ServiceToken`
for every mint after.

Every POST reads `XSRF-TOKEN` from the jar right before sending because
the site rotates it on each completed login step. A jar with no token
primes itself with a guest `GET auth/me`; a 419 re-primes and retries
once.

`NewSite` refuses plaintext off loopback and never follows a redirect.
`Site.CheckChatURL` requires wss and the site's domain for the chat URL
before anything is minted.

## `Login`: the interactive flow

`Login` owns the prompts:

- Wrong credentials re-ask the password.
- Three wrong codes or a stale challenge restart at the code prompt.
- An empty password goes back to the identity, never sent: it would
  only burn a limiter hit.
- Lockout and an undeliverable code exit with the site's message.
- A six-character code goes as `code`, anything else as
  `recovery_code`.

## Minting and signing out

`ServiceTokenMinter` is the `Minter` the client uses. A 401 from it is
`ErrSignedOut`, which the client turns into a stop and the TUI into an
exit with `run ngchat login`.

`Logout` is `POST auth/logout` with the jar: it revokes the device's
token rows on the site, which is what makes a copied cookie dead. The
CLI runs it before clearing local copies and exits non-zero if the site
did not answer.

## `Store`: what is persisted and where

`Store` persists only the `ng_remember` value: OS keyring, with a
`0600` file fallback written by atomic rewrite so an existing file's
mode is repaired. One key per site (`Site.CredentialKey`, ADR 0004);
the v0.1 cookie-header slot is deleted on sight.

`Load` reads the file before the keyring, so a value saved to the file
while the keyring was locked wins over whatever the keyring still holds
once it opens.

`Load` and `Delete` report a keyring that gave no answer as
`ErrKeyringUnavailable` rather than "not found" or success, so nothing
infers absence from it: the chat path treats it as a fresh login,
logout as a failure.
