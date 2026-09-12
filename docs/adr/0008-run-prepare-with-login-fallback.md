# One prepare step, with the login as an injected fallback

`cmd/ngchat` and `cmd/smoke` each hand-wrote the start of a run: build
the site, seed the routing cookie, take a cookie header from the
environment or else the stored remember cookie, check the chat URL,
fill a `client.Config`. The copies had drifted. Smoke read
`SMOKE_NG_COOKIE` where the TUI read `NGCHAT_NG_COOKIE`, skipped
`Store.Migrate`, could not tell a locked keyring from no login, and
had no URL defaults although `docs/smoke.md` said it ran against
production with nothing set. Neither copy was tested.

`internal/run` now holds the sequence once (`FromEnv`, `NewSite`,
`Prepare`, `ClientConfig`), tested through narrowed interfaces the way
`logout` already is. The one design question was what `Prepare` does
when the store has nothing usable: the TUI prompts a login there, and
the headless harness must not.

## Considered Options

- **`Prepare` returns a typed error and each caller decides** —
  rejected. Both callers would then re-implement the rule that
  `ErrNotFound` and `ErrKeyringUnavailable` are the same "log in"
  case (with the keyring notice before it), which is the rule
  `docs/internals/auth.md` states and the one the copies had already
  got out of step on. The rule belongs with the step.
- **`Prepare` takes the prompter and runs `auth.Login` itself** —
  rejected. The package would grow a store `Save`, the "logged in as"
  line, and a terminal dependency the harness has no use for; the
  narrowed interfaces would widen to cover it.
- **An injected `Login func(ctx) error`, nil meaning "cannot"** —
  chosen. The step owns *when* to log in; the caller owns *how*. The
  TUI passes its interactive flow. The harness passes nothing and gets
  `ErrNoCredential` wrapping the store's answer, so `errors.Is` still
  separates the keyring case, and exits 2 with `run ngchat login`.

## Consequences

- One implementation of the start of a run, 100% covered without a
  keyring or a network; a change to the order or a message happens
  once.
- `SMOKE_NG_COOKIE` is gone; smoke reads `NGCHAT_NG_COOKIE` and, like
  the TUI, defaults both URLs to production. Smoke now runs the store
  migration and reports a locked keyring as such.
- The TUI's messages keep their facts. Two wordings move: the site URL
  error now names `NGCHAT_SITE_URL` like the other variables do, and
  a bad `NGCHAT_THEME` is reported before a bad `NGCHAT_WS_URL` rather
  than after, since the theme is resolved before `Prepare` so a typo
  never waits on a login prompt.
- The channel name and the default URLs live in `run`, the one place
  both binaries read them from.
