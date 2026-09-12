# `internal/render` — HTML to ANSI, and the terminal boundary

The server ships finished HTML for each message, so this translates
tags rather than reimplementing the site's formatter; unknown tags
degrade to their text content. Emote spans become `:code:` because the
classes reference CSS sprites, not image URLs.

## Every network string is hostile to a terminal

The server escapes for a browser, not a terminal, so `Plain`/`Line`
strip C0/C1 controls and bidi overrides from every network string that
reaches the screen:

- `Text` applies it to text and attributes. Entities are decoded by
  then, so `&#27;` is a live ESC.
- hrefs must be http(s) to become OSC 8 links.
- The UI, `FailError.Message`, and `cmd/smoke` call it on usernames,
  close reasons and site messages that never pass through `Text`.

## Emoji are passed through as the server sends them

The server converts a shortname to its Unicode sequence inside a
`joypixels` span (`:wave_tone2:` becomes U+1F44B U+1F3FC), and `Text`
emits that text unchanged; no shortname parsing happens here. A
skin-tone or ZWJ sequence that reaches the screen as base glyph plus
a separate color swatch is the terminal not shaping the modifier,
not a split on our side: `printf` of the same bytes shows the same
thing. VS Code's terminal (xterm.js without its graphemes addon) is
the known case; kitty, Ghostty, WezTerm, foot, Terminal.app and iTerm2
combine it. Stripping the modifier for such terminals was rejected
because the only signal available (DEC 2027 support, which Bubble Tea
queries to choose its width method) is absent on terminals that
combine fine, so the fallback would lose tones where nothing is wrong.
