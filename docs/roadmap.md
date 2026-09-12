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
