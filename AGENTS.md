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
NGCHAT_UPSTREAM=~/dev/ngchat go generate ./internal/complete   # regen embedded lists
```

Live verification uses the headless harness in `cmd/smoke` — it runs the
full stack (mint → connect → authenticate → subscribe → send) and prints
event summaries, never tokens or cookies. With no variables set it runs
against production, which is the only option outside Newgrounds: the
dev stack below is internal to NG staff.

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
network and UI, plus three small packages the UI draws with: `theme`,
`splash` and `complete`.

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
exit with `run ngchat login`. `Logout` is `POST auth/logout` with the
jar: it revokes the device's token rows on the site, which is what
makes a copied cookie dead; the CLI runs it before clearing local
copies and exits non-zero if the site did not answer. `Store` persists
only the `ng_remember` value (OS keyring, `0600` file fallback, atomic
rewrites so an existing file's mode is repaired), under one key per
site (`Site.CredentialKey`, ADR 0004), and deletes the v0.1
cookie-header slot on sight. `Load` reads the file before the
keyring, so a value saved to the file while the keyring was locked wins
over whatever the keyring still holds once it opens; `Load` and `Delete` report a
keyring that gave no answer as `ErrKeyringUnavailable` rather than
"not found" or success, so nothing infers absence from it; the chat
path treats it as a fresh login, logout as a failure. `NewSite` refuses plaintext off loopback and never follows a
redirect; `Site.CheckChatURL` requires wss and the site's domain for the
chat URL before anything is minted. See ADR 0001, 0003
and 0004.

**`internal/protocol` — hand-ported wire types.** The source of truth is
the TypeScript Zod schemas in the private `ngchat` repo
(`src/shared/protocol/`), with no compile-time link. The asymmetry matters:
`client.go` structs are **strict** (the server rejects unknown fields, so
they must match field-for-field), while `Decode` in `server.go` is
**tolerant** — unknown names and undecodable payloads become `Unknown`,
never an error, so old binaries survive protocol additions. Protocol
changes upstream require a manual sync here (ADR 0002). The
slash-command table in `internal/complete/commands.go` is reconciled
by hand against upstream `slash_commands.ts` the same way, and its
comment names the upstream commit. The emote shortcode list in
`internal/complete/emotes_gen.go` and the emoji catalog in
`internal/complete/emoji_gen.go` extend the same drift contract, but
generated rather than hand-typed: `go generate ./internal/complete`
(`NGCHAT_UPSTREAM`, no default — see Commands) reads upstream's
`emoticons.json`/`emoticons-small.json` and
`emoji_catalog.generated.ts` and writes both files' headers naming the
upstream commit; regenerate whenever upstream's emoticon lists or
emoji catalog change.

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
20s read timeout doubles as the dead-link watchdog. The read limit is
64 MiB (`maxFrameBytes`, derived in its comment from every repeated
field of `subscribed` and a 1000-row roster assumption, since the server
caps neither the roster nor away-message length): coder/websocket's
32 KiB default is smaller than a legal `subscribed` with a few
5000-character messages in it.

**`internal/render` — HTML to ANSI, and the terminal boundary.** The
server ships finished HTML for each message, so this translates tags
rather than reimplementing the site's formatter; unknown tags degrade to
their text content. Emote spans become `:code:` because the classes
reference CSS sprites, not image URLs. The server escapes for a browser,
not a terminal, so `Plain`/`Line` strip C0/C1 controls and bidi
overrides from every network string that reaches the screen: `Text`
applies it to text and attributes (entities are decoded by then, so
`&#27;` is a live ESC), hrefs must be http(s) to become OSC 8 links, and
the UI, `FailError.Message`, and `cmd/smoke` call it on usernames, close
reasons and site messages that never pass through `Text`.

**`internal/theme` — colors by role.** A `Theme` carries the DaisyUI
roles the web client's `styles.css` declares (base, primary,
secondary, accent, neutral, info, success, warning, error) plus the
chat-only `Username`; `ngchat` and `classic` are sRGB copies of the
site's two blocks. The UI builds every style once from a theme
(`newStyles`) and names roles, never colors, so a theme is a copy of
numbers. Colors are exact; Bubble Tea's renderer downsamples them to
the terminal and drops them under `NO_COLOR`, which is why the status
bar and mention highlight also carry Reverse: the attribute survives
where the color does not. The terminal's background is never painted
(ADR 0005). `NGCHAT_THEME` picks a theme by name.

**`internal/splash` — the opening screen.** Two seams: `Art`, a
monochrome pixel bitmap drawn two pixels per row with half-block
glyphs (today the "NG CHAT" wordmark from a 5×7 font, scaled by `Fit`
to the largest of 1–4 that fits), and `Effect`, a pure function of
elapsed time (`Frame(art, palette, t)`) whose timing is fixed so a
wider terminal is not a slower splash. `LaserEtch` is the one effect:
a beam sweeps in 1.1 s, pixels cool hot → warm → ink over 0.45 s,
sparks are hashed from the frame index so frames are deterministic
and testable. A `Palette` of four roles (ink, hot, warm, spark) is
mapped from the theme, so an effect never sees a theme.

**`internal/complete` — the composer's tab-completion engine.** Pure,
like `theme` and `splash`: no Bubble Tea, no lipgloss. A `Source` is a
trigger (mention, command, emote, emoji — phases 1-4) plus the
candidate list behind it; `Engine.Complete` tries sources in order and
the first `Match` wins, so an earlier source's trigger cannot be
shadowed by a later one added down the line. Offsets are byte offsets
throughout (`Match`'s span, `Result.Start`/`End`), since Go's regexp is
byte-native; only the UI converts to and from the input widget's rune
positions. A list never opens with zero candidates, and `Dismiss`
tracks one sticky-closed span keyed by `{source, start}` so `esc`
stays quiet through further typing in the same word but clears the
moment the trigger moves or stops matching. `Rank` is the in-house,
dependency-free three-tier ranking (prefix, then substring, then
subsequence, alphabetical within a tier) every source but `Commands`
uses; commands keep the web's own prefix-plus-alias rule instead (ADR
0006), since that one was never fuzzy upstream. The command table, the
emote list and the emoji catalog are the same kind of hand-reconciled
drift ADR 0002 already documents for the protocol, with `go generate`
embedding the latter two from the upstream checkout (phases 3-4).
`Mentions` (phase 1) is the first source: it ranks the live roster
rather than a fixed list, reading `Users`/`Self` fresh on every
`Candidates` call so a join, a leave or an away change between
keystrokes needs no change notification into this package. `Commands`
(phase 2) triggers on the whole line rather than a trailing word (`/`
only opens the list as the first character typed), and filters
`CommandTable` to the viewer's rank, read fresh from `Viewer` on every
query so a `revalidated` rank change takes effect without a hook into
this package either; that filter is cosmetic, as upstream documents —
the server is the only enforcement point, and hiding a command here
only keeps the list from advertising a dead end. `Emotes` (phase 3)
has no fields, unlike `Mentions` and `Commands`: `emoteCodes` is
static between builds, so there is nothing to read fresh. Its trigger
needs whitespace or the line start immediately before `ng`/`tf` (RE2
has no lookbehind, so a leading `(?:^|\s)` stands in for upstream's
`(?<= |^)`), which is what keeps `@ngfoo` a mention rather than an
emote. A `*` typed in the term is stripped before ranking rather than
given wildcard semantics of its own: the shared `Rank`'s subsequence
tier already treats the gap as "anything", which is the same
approximation upstream's fuzzysort gives it. `Emoji` (phase 4) is the
same shape as `Emotes`, over `emojiCatalog` (`[]emoji{Name, Glyph}`)
instead of `emoteCodes`; its trigger is `\B:([+0-9a-z][-+_*0-9a-z]+)$`,
upstream's own regex, where `\B` is what keeps `http://` closed and the
two-character minimum is what keeps `:)`/`:D`/`:-)` typeable. Each
candidate's row pairs `glyphCell` — the glyph padded to a fixed 3-cell
column with `ansi.StringWidth`, which already accounts for ZWJ
sequences and variation selectors — with the shortname, so the
shortname starts at the same column for emoji-presentation glyphs
(a wide family/couple glyph and a narrow one alike). `ansi.StringWidth`
under-reports a skin-tone modifier sequence (`:woman_lifting_weights_tone1:`,
U+1F3FB..U+1F3FF) as 1 cell where UTS #51 mandates emoji presentation
(2 cells); `glyphCell` corrects for that one known case. A handful of
unrelated glyphs (`:detective:` among them) still render narrower than
the column expects in some terminals for reasons outside this
function's control; eyeball `:woman_lifting_weights_tone1:` and
`:detective:` specifically when checking alignment on a new terminal.

**`internal/ui` — Bubble Tea.** `waitEvent` pumps one `client.Event` into
the tea loop and reschedules itself, which is how the network goroutine and
the UI loop stay decoupled. Transcript rows keep the raw `html` and convert
on render, so toggling spoilers (`ctrl+s`) re-renders from source. Scrollback
is the viewport's, not the terminal's. A re-render (`refresh`) takes an
`anchor` read off the viewport beforehand (`locate`): the bottom, or the
row under the top of the screen plus lines into it, because a toggle or
a resize changes row heights and a line offset alone would slide to a
different row; `push` adjusts the anchor for rows the cap trimmed. The
web's "more messages below" control is the help row while the reader
is scrolled up (`helpLine`), rather than a row of its own or an overlay:
neither resizes the viewport under the reader nor covers a line they
paged to. `end` jumps back when scrolled up and stays the composer's
otherwise; typing a character or sending also resumes following, as on
the web. While `Model.splash` is non-nil
the view is the splash (centered frame, state label, skip hint) and
the chat layout is kept current underneath; `frameMsg` ticks it at
30 fps, and it ends when the effect is done and the client is online,
at `maxSplash` (4 s) regardless, on any key (consumed; `ctrl+c` still
quits), or at once when the terminal is too small to fit the wordmark.
A stop that leaves (signed out, access denied) quits through the
splash like it does through the chat screen. `Model.completion` holds
the open completion list (nil when closed); while it is open, `tab`,
`shift+tab` and the up/down arrows cycle and `enter`/`esc` accept or
dismiss without recomputing, so the candidate set cannot change out
from under the highlighted index mid-navigation — every other key
falls through to the input and recomputes after (left/right move the
cursor as usual). Below 3 spare viewport rows the list does not draw
at all (no-list mode): cycling instead previews the highlighted
candidate directly in the line, trailing space trimmed, and accept
adds it back. A resize reclamps the completion window (`clampWindow`)
so a stale scroll position from before the resize cannot walk the
list's row draw past the end of its candidates. `completionSources`
builds the real source list fresh from the current `*Model` on every
call rather than once in `New`, because `Model` is a value type Bubble
Tea copies on every `Update`: a slice closed over the model as it stood
in `New` would keep reading that copy's `self` *and* its `users` map
forever — `Subscribed` replaces `users` wholesale with a new map, so
the captured copy would stay stuck on the empty one `New` made.

## Conventions

- Every exported type, function, and package carries a doc comment
  explaining *why*, not what. Match that density; comments here earn their
  place by naming a constraint or a gotcha.
- Decisions with tradeoffs go in `docs/adr/` as short prose ADRs (context →
  options → consequences). Domain vocabulary lives in `CONTEXT.md` — use
  those terms ("re-mint", "backfill buffer", "gap", not "refresh",
  "scrollback", "history").
- `docs/site-login-endpoints.md` is the site contract `internal/auth`
  and its `httptest` fakes follow; the site itself is a separate
  codebase. `docs/roadmap.md` lists what is out of scope and planned.
- Secrets never touch stdout or logs; `cmd/smoke` prints summaries only.
- Tests are table-driven and hermetic: no network (use `httptest`), and no
  writes to the real OS keyring or config dir (override `XDG_CONFIG_HOME`
  and exercise the file fallback). Terminal styling is stripped with an ANSI
  regexp before comparing, so assertions hold under any color profile.
