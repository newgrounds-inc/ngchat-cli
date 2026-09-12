# Roadmap

What `v1.0.0` deliberately leaves out, and what is planned after it.
Decisions with tradeoffs get an ADR when they land; this list is only
the intent.

## Not in v1.0.0

- Channel switching (there is one channel, `general`)
- Inline re-login inside the TUI (a signed-out run exits and says `run
  ngchat login`)
- Images and emote sprites: NG emotes render as `:code:`, since the
  site marks them up as CSS sprite classes with no image URL. Unicode
  emoji are plain text and already render; `:shortname` completes
  them from an embedded catalog.
- Sound playback
- Embeds: the `messageEmbeds` frame and the `embeds` field on messages
  are not decoded; a link-preview line under the message is the
  plausible TUI shape
- Markdown composition (`isMarkdown` on outbound messages)

## After v1.0.0

- **Kitty graphics** for memes and emote sprites. The protocol supports
  frame-based animation (kitty 0.20+), so GIF and APNG play in kitty
  itself; Ghostty and WezTerm draw stills. Needs an emote code→image
  source from the site and a URL fetch path in the render layer.
- **Soundboard**: first the narrated `X played Y` line and a `/sfx`
  list; real audio investigated later with `CGO_ENABLED=0` as a hard
  constraint on the static binary. Until then the narrated line shows
  for a sound nobody heard: upstream tags it with `triggerId` so a
  client that drops `playSoundboardTrigger` can hide the row too, and
  this client decodes neither.
- User list as a side pane once the UI gets a real pass.
- Homebrew tap once Releases are steady.

## Deepening candidates

From the September 2026 architecture review. Candidate 1, the
transcript package, landed as ADR 0007. The rest, in the order worth
taking them:

- **One "prepare a run" step for `cmd/ngchat` and `cmd/smoke`** (strong,
  cheapest). Site construction, the cookie-header override vs the
  stored remember cookie, `CheckChatURL` and `client.Config` are
  written twice and have already diverged (`NGCHAT_NG_COOKIE` vs
  `SMOKE_NG_COOKIE`; smoke skips `Store.Migrate` and cannot tell
  keyring-unavailable from not-found). Both copies are untested; give
  `prepareSite` the narrowed-interface treatment `logout` has.
- **Fold the composer out of `Model`** (strong). Twenty methods and
  five fields exist only because the completion list's state is split
  between `completionState`, the textinput and `Model.height`;
  `clampWindow`'s doc comment lists every caller that must reclamp. A
  composer that owns the textinput, the engine and the open list takes
  a key and a spare-row count. ADR 0006's pure engine is untouched.
- **`client.Event` as a closed set** (worth exploring). Today it is
  `{Msg any, State, Gap, Backfill, Err}` with the co-occurrence rules
  in comments; `ui.handleEvent` and `cmd/smoke` each re-do the same
  14-way type switch and the UI holds 23 `protocol.*` references. If
  the client emitted classified, sanitized chat events, a protocol
  change (ADR 0002) would stop at `client`, and `Init`/`waitEvent`
  (0% covered) could be driven through `Update`.
- **`render` colors by theme role** (worth exploring, small). Links,
  inline code and emote codes still use ANSI slots 11/12/13 next to a
  themed speaker name; the gap ADR 0005 left. Build the renderer from
  three roles the way `splash.Palette` is mapped from a theme.
- **Inject a clock into `client.session`** (speculative). The renewal
  budget and reconnect backoff read real time, so the suite spends
  ~5 s in sleeps and `quietFor(200ms)` silences. Shape is fine; only
  the clock needs a seam.
