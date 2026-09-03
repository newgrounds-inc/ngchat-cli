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

**Theme**:
A named palette of *roles* (base, primary, secondary, accent, neutral,
info, success, warning, error, username), the same roles the web
client's DaisyUI theme declares. Widgets ask for a role, never a
color. `ngchat` is the site's; `classic` is the legacy gold-on-black.

**Role**:
One named slot in a theme, with a meaning ("accent is what must stand
out") decided once for every theme. Not a color: the color is what a
theme puts in the slot.

**Splash**:
The opening screen: an *art* drawn in by an *effect* while the client
connects. Ends when the effect finishes and the client is online, or
after four seconds, or on any key.

**Art**:
A monochrome pixel bitmap the splash draws, two pixels per terminal
row with half-block glyphs. Today the "NG CHAT" *wordmark*.

**Effect**:
How an art is drawn in over time: a pure function of elapsed time
that returns one frame. Today *laser etch*, a beam that sweeps left to
right and ignites the pixels it passes.

**Trigger**:
The character sequence that opens a completion list: `@` for mentions,
`/` for commands, the `ng`/`tf` emote prefixes, `:` for emoji. Matched
against the line up to the cursor only, never past it.
_Avoid_: autocomplete

**Span**:
The run of the line a completion will replace on accept: from the
trigger to the cursor. Byte offsets throughout, since Go's regexp is
byte-native and only the UI ever converts to the input widget's rune
positions.
_Avoid_: word, selection

**Candidate**:
One row a completion list offers: an `Insert` (the full replacement,
trailing space included where the wire format wants one), a `Label`
and an optional dimmed `Detail`.
_Avoid_: suggestion, option

**Source**:
One trigger and the candidate list behind it (mentions, commands,
emotes, emoji are the four phases). Stateless between calls: the
engine re-queries a source on every keystroke rather than caching,
since what it reads (the roster, the privilege flags) can change
underneath it.

**Dismiss**:
Closing a completion list with `esc`. Sticky per span
(`{source, start}`): typing further into the same span stays quiet
until the trigger's start moves or a keystroke matches something else,
so a list is never reopened by the same word it was just closed on.
_Avoid_: history (a dismissal is not remembered past the span moving)

