# `internal/run` — the one "prepare a run" step

Both binaries start a run the same way, and this package is where that
sequence lives so it drifts in one place (ADR 0008). `cmd/ngchat` and
`cmd/smoke` call it and nothing else builds a site, seeds a jar or
fills a `client.Config`.

## The sequence

`FromEnv` reads the four variables both binaries honor
(`NGCHAT_SITE_URL`, `NGCHAT_WS_URL`, `NGCHAT_ROUTING_COOKIE`,
`NGCHAT_NG_COOKIE`) into an `Env` value, defaulting the URLs to
production. Everything after takes the value, so no test reads the
environment.

`NewSite` is separate from `Prepare` because `login` and `logout` need
the site too, and a stale chat URL or a missing credential must never
block either. It seeds the routing cookie so it rides on every request.

`Prepare` runs in this order, and the order is the point:

1. `CheckChatURL`, before anything can mint: the JWT minted from the
   site's cookie goes to that URL as the first frame.
2. `NGCHAT_NG_COOKIE`, when set, is seeded as-is and the store is not
   read. A stale export therefore wins over a fresh login, which is
   why `Prepare` returns the `Source` and the smoke harness prints it.
3. Otherwise `Store.Migrate` removes the v0.1 cookie-header slot (with
   a notice, so a later login prompt is not a surprise), the remember
   cookie is loaded, and the jar is seeded with it.

`ClientConfig` is the `client.Config` both binaries run: the chat URL
and routing cookie from `Env`, the one `Channel`, a
`ServiceTokenMinter` over the site `Prepare` just seeded.

## The login fallback

`ErrNotFound` and `ErrKeyringUnavailable` from `Load` both mean "log
in": there is nothing usable this run, and a fresh login lands in the
file, which `Load` reads first from then on (`docs/internals/auth.md`).
The keyring case gets a notice naming the cause; the not-found case is
silent, since the login prompt is the message.

What "log in" does is the caller's: `Options.Login` runs it, and must
leave the jar authenticated (`auth.Login` does). The TUI passes its
interactive flow. The smoke harness passes nothing, and `Prepare`
returns `ErrNoCredential` wrapping the store's answer, so
`errors.Is` still tells a locked keyring from no login and the operator
sees which. Any other `Load` or `Migrate` error propagates untouched.

## Messages

Notices go to `Options.Notices` (stderr for the TUI, stdout for smoke),
prefixed `ngchat:` so they read like the rest of the tool's output.
Errors name the variable at fault (`NGCHAT_WS_URL: ...`), since the fix
is an `export`. Nothing written here is ever a secret: the header and
the remember value are seeded, never echoed.

## Testing

`CredentialStore` and `Site` are the narrowed interfaces, the same
treatment `logout` in `cmd/ngchat` has. The tests script the decision
table with fakes and never touch a keyring, a config file or the
network; the site fake delegates `CheckChatURL` to a real `*auth.Site`
so the domain rule under test is the real one. `NewSite` and
`ClientConfig` are tested against real `auth` values, which are pure
until a request is made.
