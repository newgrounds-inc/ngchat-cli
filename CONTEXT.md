# NG Chat CLI

A minimalist terminal client for Newgrounds Chat: single static Go binary,
fullscreen TUI, talking the same WebSocket protocol as the browser client.

## Language

**Chat JWT**:
The 1-hour HS256 token minted by the main site that authenticates a chat
connection. Presented as the first WebSocket message, not a header.
_Avoid_: session token, API key

**Remember Cookie**:
The value of the site's 400-day `ng_remember` cookie, set by a
`remember=true` login. The only secret ever persisted — never the
account password, never the site session.
_Avoid_: password, login token, session cookie

**Cookie Jar**:
The in-memory cookie store for one run: seeded with the remember cookie,
then holding the site session and `XSRF-TOKEN` cookies that priming
sets. Dies with the process.

**Prime**:
A guest `GET /api/v1/auth/me` whose only purpose is the session and CSRF
cookies it sets; its 401 is expected. Once per run before the first
POST, and again after a 419.

**Re-mint**:
Obtaining a fresh chat JWT from the site with the cookie jar
(`POST /api/v1/auth/service-token`). Once per connect and once per
revalidate, never in a loop: the site's limiter counts before auth.
_Avoid_: refresh, renew

**Signed Out**:
A 401 from the re-mint: the remember cookie no longer works because the
password changed or `ngchat logout` ran. Final; the TUI exits and the
user runs `ngchat login`.
_Avoid_: expired, unauthorized

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
