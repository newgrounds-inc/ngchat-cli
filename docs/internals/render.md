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
