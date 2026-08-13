# AGENTS.md

This file provides guidance to coding agents when working with code in this
repository. `CLAUDE.md` imports it so Claude Code picks it up too.

## Commands

```sh
go build ./cmd/ngchat            # build the binary
go vet ./...                     # vet everything
go test ./...                    # no tests exist yet; add them as *_test.go
go test -run TestName ./internal/render   # single test, once tests exist
goreleaser release --snapshot --clean     # local cross-platform build check
```

Live verification uses the headless harness in `cmd/smoke` — it runs the
full stack (mint → connect → authenticate → subscribe → send) and prints
event summaries, never tokens or cookies:

```sh
export NGCHAT_WS_URL=wss://chat.newgrounds-d.com/ws
export NGCHAT_JWT_URL=https://www.newgrounds-d.com/ngapps/jwt.php
export NGCHAT_ROUTING_COOKIE='serverid=...'   # dev proxy routing, every request
export SMOKE_NG_COOKIE='...'                  # NG session/remember cookie
go run ./cmd/smoke
```

The TUI takes over the screen, so `cmd/smoke` is the right tool for
verifying protocol or auth changes; reserve `cmd/ngchat` for UI work.

## Architecture

Four layers, each one package, with a channel as the only seam between
network and UI.

**`internal/auth` — credentials and chat JWTs.** `Minter` is a one-method
interface with two implementations: `CookieMinter` (GET `jwt.php` with an
NG cookie — the long-term path) and `PasswordMinter` (POST with
username/password — interim, allowlisted bot accounts only). `Store`
persists the NG remember cookie in the OS keyring, falling back to a
`0600` JSON file. The account password is never persisted; see ADR 0001.

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

- *Hourly JWT bounce.* The chat JWT lives 1 hour and the server hard-closes
  the socket when it lapses. Reconnect + backfill dedupe (`markSeen`, a
  500-ID window) make that invisible to the UI.
- *Gap detection.* `Subscribed` carries ≤25 recent events, the only history
  a client ever gets. If a reconnect's backfill shares no IDs with what was
  already displayed, `Event.Gap` is set and the loss is surfaced, not
  papered over. The buffer arrives newest-first and is reversed on replay.
- *Stop vs retry.* `stopError` marks final conditions (close reasons
  `client close` / `server close` / `idle timeout`, kicks, hard auth
  failures). `unauthorized` containing "expired"/"malformed" is *soft*:
  re-mint and re-send `authenticate` on the same socket, up to 3 times.

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
