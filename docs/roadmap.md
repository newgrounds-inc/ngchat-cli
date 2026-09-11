# Roadmap

What `v1.0.0` deliberately leaves out, and what is planned after it.
Decisions with tradeoffs get an ADR when they land; this list is only
the intent.

## Not in v1.0.0

- Channel switching (there is one channel, `general`)
- Inline re-login inside the TUI (a signed-out run exits and says `run
  ngchat login`)
- Images and emote sprites (emotes render as `:code:`)
- Sound playback
- Rich embed layout (`messageEmbeds` renders at most as text)
- Markdown composition (`isMarkdown` on outbound messages)
- Passwordless email-only accounts

## After v1.0.0

- **Kitty graphics** for memes and emote sprites. The protocol supports
  frame-based animation (kitty 0.20+), so GIF and APNG play in kitty
  itself; Ghostty and WezTerm draw stills. Needs an emote code→image
  source from the site and a URL fetch path in the render layer.
- **Soundboard**: first the narrated `X played Y` line and a `/sfx`
  list; real audio investigated later with `CGO_ENABLED=0` as a hard
  constraint on the static binary.
- User list as a side pane once the UI gets a real pass.
- Homebrew tap once Releases are steady.
