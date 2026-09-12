# `internal/client` — the session state machine

`Run` loops sessions with exponential backoff; `session` does mint →
dial → `authenticate` → `getChannelID` → `subscribe`, then reads frames
until the socket dies. Vocabulary (re-mint, revalidate, backfill
buffer, gap) is defined in `CONTEXT.md`.

## Hourly renewal in place

The chat JWT lives 1 hour. Two minutes before its hard-close deadline
the server sends `revalidate`; the client re-mints once and answers
`reauthenticate`, and `revalidated` confirms with the new privilege
flags.

Exactly one attempt per nudge: the server caps attempts per token and
the site's mint limiter counts before auth, so a loop would lock the
user's browser out too.

A failed mint is a `RenewalFailed` event, never fatal, because the
deadline never moves: the server still closes the socket and the
reconnect + backfill dedupe (`markSeen`, a 500-ID window) path takes
over, as it did before renewal existed.

## Gap detection

`Subscribed` carries ≤25 recent events, the only history a client ever
gets. If a reconnect's backfill shares no IDs with what was already
displayed, `Event.Gap` is set and the loss is surfaced, not papered
over. The buffer arrives newest-first and is reversed on replay.

## Stop vs retry

`stopError` marks final conditions: close reasons `client close` /
`server close` / `idle timeout`, the `kicked` and `idleTimeout` frames
themselves, hard auth failures.

Before `authenticated`:

- An `unauthorized` containing "expired"/"malformed" is *soft*: re-mint
  and re-send `authenticate` on the same socket, up to 3 times.
- Any other `unauthorized` is the server's entry gate (not a supporter,
  under 18, e-mail not validated, banned). The stop wraps
  `AccessDenied` carrying the server's HTML notice, which the TUI
  renders and prints on exit, the same way a signed-out run prints
  `run ngchat login`.

After `authenticated`:

- An `unauthorized` with a `channelID` is a refused subscribe (channel
  ban, alt of a banned account). The server leaves the socket open, but
  with one channel there is nothing left to join, so it is the same
  `AccessDenied` stop.
- Any other `unauthorized` is never answered: it only precedes a close,
  either token expiry (reason `token expired`, which reconnects) or a
  rejected `reauthenticate`.

## Heartbeat and frame size

Heartbeat pings every 5s; the server drops sockets silent for 15s, and
the 20s read timeout doubles as the dead-link watchdog.

The read limit is 64 MiB (`maxFrameBytes`, derived in its comment from
every repeated field of `subscribed` and a 1000-row roster assumption,
since the server caps neither the roster nor away-message length).
coder/websocket's 32 KiB default is smaller than a legal `subscribed`
with a few 5000-character messages in it.
