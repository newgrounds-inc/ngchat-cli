# AGENTS.md

This file provides guidance to coding agents when working with code in this
repository. `CLAUDE.md` imports it so Claude Code picks it up too.

## Commands

```sh
go build ./cmd/ngchat            # build the binary
go vet ./...                     # vet everything
go test ./...                    # unit tests (no network, no keyring)
go test -run TestDecodeTolerance ./internal/protocol   # a single test
go test -cover ./internal/...    # coverage per package
goreleaser release --snapshot --clean     # local cross-platform build check
```

Live verification uses the headless harness in `cmd/smoke` — it runs the
full stack (mint → connect → authenticate → subscribe → send) and prints
event summaries, never tokens or cookies:

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_SITE_URL=https://www.newgrounds-d.com
export NGCHAT_ROUTING_COOKIE='serverid=bcolby2'   # dev proxy routing, every request
export SMOKE_NG_COOKIE='ng_remember=...'      # raw cookie header; optional
export SMOKE_SECONDS=150                      # optional; default 15
go run ./cmd/smoke
```

The routing cookie is not a secret: it names the dev backend the proxy
should pin you to, and `bcolby2` is the one this project's dev stack
runs on (verified 2026-09-02; without it dev answers 503 for every
request).

Without `SMOKE_NG_COOKIE` the harness uses the remember cookie that
`ngchat login` stored, which is how the login flow is verified end to
end: `NGCHAT_SITE_URL=... ngchat login`, then `go run ./cmd/smoke`. An
exported `SMOKE_NG_COOKIE` wins over the stored login, so `unset` it
first; the first smoke line names which source it used.

To watch a token renewal, set `APP_JWT_CHAT_TTL` to 180 seconds on the
dev site and run with `SMOKE_SECONDS=200`: expect a `revalidated` line
about every 70s and no `state=2` (reconnecting) line. The TTL must stay
above the server's two-minute nudge lead: a shorter one makes the server
nudge again the instant each renewal lands, the client's 5/min renewal
budget then refuses the rest, and the token expires into a reconnect.

The TUI takes over the screen, so `cmd/smoke` is the right tool for
verifying protocol or auth changes; reserve `cmd/ngchat` for UI work.

## Architecture

Four layers, each one package, with a channel as the only seam between
network and UI.

**`internal/auth` — credentials, site login, chat JWTs.** `Site` wraps
the site's `/api/v1/auth` routes with one in-memory cookie jar per run:
`Login` and `TwoFactor` for the first run, `ServiceToken` for every mint
after. Every POST reads `XSRF-TOKEN` from the jar right before sending
because the site rotates it on each completed login step; a jar with no
token primes itself with a guest `GET auth/me`, and a 419 re-primes and
retries once. `Login` (the flow) owns the prompts: wrong credentials
re-ask the password, three wrong codes or a stale challenge restart
there, an empty password goes back to the identity (never sent: it
would only burn a limiter hit), lockout and an undeliverable code exit
with the site's message. A
six-character code goes as `code`, anything else as `recovery_code`.
`ServiceTokenMinter` is the `Minter` the client uses; a 401 from it is
`ErrSignedOut`, which the client turns into a stop and the TUI into an
exit with `run ngchat login`. `Store` persists only the `ng_remember`
value (OS keyring, `0600` file fallback) and deletes the v0.1
cookie-header slot on sight. See ADR 0001 and ADR 0003.

**`internal/protocol` — hand-ported wire types.** The source of truth is
the TypeScript Zod schemas in the private `ngchat` repo
(`src/shared/protocol/`), with no compile-time link. The asymmetry matters:
`client.go` structs are **strict** (the server rejects unknown fields, so
they must match field-for-field), while `Decode` in `server.go` is
**tolerant** — unknown names and undecodable payloads become `Unknown`,
never an error, so old binaries survive protocol additions. Protocol
changes upstream require a manual sync here (ADR 0002).

**`internal/client` — the session state machine.** `Run` loops sessions
with exponential backoff; `session` does mint → dial → `authenticate` →
`getChannelID` → `subscribe`, then reads frames until the socket dies.
Three behaviors are load-bearing:

- *Hourly renewal in place.* The chat JWT lives 1 hour. Two minutes before
  its hard-close deadline the server sends `revalidate`; the client
  re-mints once and answers `reauthenticate`, and `revalidated` confirms
  with the new privilege flags. Exactly one attempt per nudge: the server
  caps attempts per token and the site's mint limiter counts before auth,
  so a loop would lock the user's browser out too. A failed mint is a
  `RenewalFailed` event, never fatal, because the deadline never moves:
  the server still closes the socket and the reconnect + backfill dedupe
  (`markSeen`, a 500-ID window) path takes over, as it did before renewal
  existed.
- *Gap detection.* `Subscribed` carries ≤25 recent events, the only history
  a client ever gets. If a reconnect's backfill shares no IDs with what was
  already displayed, `Event.Gap` is set and the loss is surfaced, not
  papered over. The buffer arrives newest-first and is reversed on replay.
- *Stop vs retry.* `stopError` marks final conditions (close reasons
  `client close` / `server close` / `idle timeout`, the `kicked` and
  `idleTimeout` frames themselves, hard auth failures). Before
  `authenticated`, an `unauthorized` containing "expired"/"malformed" is
  *soft*: re-mint and re-send `authenticate` on the same socket, up to 3
  times. Any other pre-auth `unauthorized` is the server's entry gate
  (not a supporter, under 18, e-mail not validated, banned): the stop
  wraps `AccessDenied` carrying the server's HTML notice, which the TUI
  renders and prints on exit, the same way a signed-out run prints
  `run ngchat login`. After `authenticated`, an `unauthorized` with a
  `channelID` is a refused subscribe (channel ban, alt of a banned
  account): the server leaves the socket open, but with one channel
  there is nothing left to join, so it is the same `AccessDenied` stop.
  Any other post-auth `unauthorized` is never answered: it only precedes
  a close, either token expiry (reason `token expired`, which
  reconnects) or a rejected `reauthenticate`.

Heartbeat pings every 5s; the server drops sockets silent for 15s, and the
20s read timeout doubles as the dead-link watchdog.

**`internal/render` — HTML to ANSI.** The server ships finished HTML for
each message, so this translates tags rather than reimplementing the site's
formatter; unknown tags degrade to their text content. Emote spans become
`:code:` because the classes reference CSS sprites, not image URLs.

**`internal/ui` — Bubble Tea.** `waitEvent` pumps one `client.Event` into
the tea loop and reschedules itself, which is how the network goroutine and
the UI loop stay decoupled. Transcript rows keep the raw `html` and convert
on render, so toggling spoilers (`ctrl+s`) re-renders from source. Scrollback
is the viewport's, not the terminal's.

## Conventions

- Every exported type, function, and package carries a doc comment
  explaining *why*, not what. Match that density; comments here earn their
  place by naming a constraint or a gotcha.
- Decisions with tradeoffs go in `docs/adr/` as short prose ADRs (context →
  options → consequences). Domain vocabulary lives in `CONTEXT.md` — use
  those terms ("re-mint", "backfill buffer", "gap", not "refresh",
  "scrollback", "history").
- `docs/site-login-endpoints.md` is a working brief for the *site* repo,
  not work to do here.
- Secrets never touch stdout or logs; `cmd/smoke` prints summaries only.
- Tests are table-driven and hermetic: no network (use `httptest`), and no
  writes to the real OS keyring or config dir (override `XDG_CONFIG_HOME`
  and exercise the file fallback). Terminal styling is stripped with an ANSI
  regexp before comparing, so assertions hold under any color profile.
