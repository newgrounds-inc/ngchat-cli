# NG Chat CLI

A minimalist terminal client for Newgrounds Chat: single static Go binary,
fullscreen TUI, talking the same WebSocket protocol as the browser client.

## Language

**Chat JWT**:
The 1-hour HS256 token minted by the main site that authenticates a chat
connection. Presented as the first WebSocket message, not a header.
_Avoid_: session token, API key

**Remember Cookie**:
The long-lived newgrounds.com credential the CLI stores to re-mint chat
JWTs. The only secret ever persisted — never the account password.
_Avoid_: password, login token

**Re-mint**:
Obtaining a fresh chat JWT from the site mid-session. Recurs hourly
because the chat server hard-closes sockets when the JWT lapses.
_Avoid_: refresh, renew

**Revalidate**:
The server's nudge two minutes before the hard-close deadline. The client
re-mints and answers with `reauthenticate`; the server swaps the socket's
claims in place and replies `revalidated`. Ignoring it costs nothing but
the reconnect. The deadline itself never moves.
_Avoid_: refresh, keep-alive

**Channel**:
A named, server-defined chat room joined by name lookup then numeric-ID
subscription. Clients cannot create channels.
_Avoid_: room

**Backfill Buffer**:
The ≤25 recent events delivered on subscribe — the only history a client
ever receives. There is no history API.
_Avoid_: scrollback, history

**Gap**:
A reconnect whose backfill buffer shares no message IDs with what was
already displayed: history was lost and is accepted as lost.

**Heartbeat**:
The application-level ping/pong keeping the socket alive; the server
drops any socket with no inbound traffic for 15 seconds.
_Avoid_: keepalive

**Emote Code**:
A bare short-code (e.g. `ngaHoldup`) the server marks up as a CSS sprite
class. Clients map code→image themselves; this client renders `:code:`
until the image pipeline lands.
_Avoid_: emoji, emoticon

**Embed**:
A server-side unfurl of a URL in a message, pushed as a separate
follow-up event that clients attach to the original message by ID.

**Spoiler**:
A message flagged to stay hidden until the reader deliberately reveals
it.
