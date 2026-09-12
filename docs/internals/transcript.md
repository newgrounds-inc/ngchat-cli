# `internal/transcript` — the chat log and the reader's place

The transcript owns its rows *and* the viewport they are read through
(ADR 0007). Callers append a row, resize, set the reveal and timestamp
options, page, and follow; where the reader is between those calls is
the package's business.

## Rows

A row arrives classified (`Kind`) and stamped (`At`); the transcript
never reads the clock or the network. `Username` and `HTML` are treated
as hostile and sanitized on every draw (`render.Line`, `render.Text`);
`Text` is trusted as the caller rendered it, so a caller building an
event row from a wire string sanitizes it first. HTML rows keep their
source and convert on draw, so revealing a spoiler shows what was
hidden rather than a placeholder that was rendered once.

`Roster` exists because the `/who` listing carries its own dimming for
away users and must not be wrapped in the event style on top.

## The anchor

A reveal, a timestamp toggle, or a new width changes how many screen
lines each row takes, so the reader's place is held as an `anchor`: the
bottom (following), or the row under the top of the screen plus lines
into it. Every mutation calls `locate` first and `show` restores the
anchor after; the cap trim shifts the anchored row by however many
rows fell off. `within` is clamped to the row's new height so a row
that shrank does not push the screen into the row after it.

`Append` renders only the new row and extends the line table; a toggle
or a new width rebuilds every row. A height-only `Resize` (the
completion list opening or closing) keeps the content and reclamps:
`viewport.SetHeight` (bubbles v2.2.1) does not, so a reader at the
bottom stays there and one left past the bottom by a shrink snaps
back.

The viewport keeps the line slice it is handed, so `show` gives it a
clone; later appends must not reach into its copy. `maxLineWidth`
inside `SetContentLines` walks every line, so a sized append is O(rows)
in the viewport even though the transcript's own work is one row. The
cap (`MaxRows`, 2000) bounds that.

## Testing

Tests drive the interface (`Append`, `Resize`, `SetOptions`, `PageUp`,
`Follow`, `View`) and read `Following()` and `Rows()`. The two internal
seams the tests do use are `locate` (which row is under the top) and
the viewport's offset, for the reclamp cases that need to stand one
line above the bottom. Styling is compared with ANSI stripped, as
everywhere else.
