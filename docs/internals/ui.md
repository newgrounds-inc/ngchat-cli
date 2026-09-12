# `internal/ui` — Bubble Tea

`waitEvent` pumps one `client.Event` into the tea loop and reschedules
itself, which is how the network goroutine and the UI loop stay
decoupled.

## Transcript and reader position

Transcript rows keep the raw `html` and convert on render, so toggling
spoilers (`ctrl+s`) re-renders from source. Scrollback is the
viewport's, not the terminal's.

A re-render (`refresh`) takes an `anchor` read off the viewport
beforehand (`locate`): the bottom, or the row under the top of the
screen plus lines into it. A toggle or a resize changes row heights, so
a line offset alone would slide to a different row; `push` adjusts the
anchor for rows the cap trimmed.

The web's "more messages below" control is the help row while the
reader is scrolled up (`helpLine`), rather than a row of its own or an
overlay: neither resizes the viewport under the reader nor covers a
line they paged to. `end` jumps back when scrolled up and stays the
composer's otherwise; typing a character or sending also resumes
following, as on the web.

## Splash

While `Model.splash` is non-nil the view is the splash (centered frame,
state label, skip hint) and the chat layout is kept current underneath.
`frameMsg` ticks it at 30 fps. It ends when the effect is done and the
client is online, at `maxSplash` (4 s) regardless, on any key
(consumed; `ctrl+c` still quits), or at once when the terminal is too
small to fit the wordmark. A stop that leaves (signed out, access
denied) quits through the splash like it does through the chat screen.
The art and effect themselves are `internal/splash` (`splash.md`).

## Completion list

`Model.completion` holds the open completion list (nil when closed).
While it is open, `tab`, `shift+tab` and the up/down arrows cycle and
`enter`/`esc` accept or dismiss without recomputing, so the candidate
set cannot change out from under the highlighted index mid-navigation.
Every other key falls through to the input and recomputes after
(left/right move the cursor as usual).

Below 3 spare viewport rows the list does not draw at all (no-list
mode): cycling instead previews the highlighted candidate directly in
the line, trailing space trimmed, and accept adds it back.

A resize reclamps the completion window (`clampWindow`) so a stale
scroll position from before the resize cannot walk the list's row draw
past the end of its candidates.

`completionSources` builds the real source list fresh from the current
`*Model` on every call rather than once in `New`, because `Model` is a
value type Bubble Tea copies on every `Update`: a slice closed over the
model as it stood in `New` would keep reading that copy's `self` *and*
its `users` map forever. `Subscribed` replaces `users` wholesale with a
new map, so the captured copy would stay stuck on the empty one `New`
made. The engine itself is `internal/complete` (`complete.md`).
