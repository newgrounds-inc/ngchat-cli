# Tab completion: a pure engine, a list above the input

`tab` reached the textinput widget unbound. Bubbles' own suggestion
feature was inert underneath it — never fed, and it matches the whole
line rather than the word at the cursor — so the composer had no way to
complete a mention, a slash command, an NG "dank meme" emote, or an
emoji shortname the way the web client does. Phase 0 builds the engine
and the list with no real sources yet; mentions, commands, emotes and
emoji follow as their own phases, each adding a `complete.Source`.

## Considered Options

- **One engine, sources behind a small interface** — chosen. Same split
  as `render`, `theme` and `splash`: a pure package (`internal/complete`)
  takes a line and a cursor offset and returns a span and candidates,
  and the UI wires in the roster, the privilege flags and the drawing.
  A source only has to implement `Match` and `Candidates`.
- **Inline cycling (replace the word in place, no list)** — rejected.
  Emotes and slash commands are exactly the things a person does not
  know the full name of; seeing the alternatives is the point. The list
  is capped at 10 candidates, shows 5, and scrolls with the highlight.
- **fuzzysort, the web client's ranker** — rejected as a dependency
  for a CLI that otherwise has none. The in-house `Rank` is
  deterministic: case-insensitive prefix matches first, then substring,
  then subsequence, alphabetical within a tier. Slash commands keep the
  web's exact rule instead (prefix with a `*` wildcard, name and
  aliases, alphabetical) since that one was never fuzzy upstream.
- **Bare-word nick completion** (complete any word, not just `@name`)
  — deferred. The web only opens a dropdown on trigger characters
  (`@`, `/`, `:`, the `ng`/`tf` emote prefixes); matching that keeps
  muscle memory intact and avoids a list popping up on every word. It
  can be added as a fifth source later if missed.
- **Show the raw roster for mentions, like the web does** — rejected in
  one respect: completing yourself is never useful, so self is excluded
  from the candidate list. Away users are kept, just dimmed.
- **Fetch command/emote/emoji lists from the site at startup** —
  rejected. They change a few times a year; embedding them as generated
  Go source (`go generate`, committed) avoids both the latency and the
  failure mode of a fetch, extending ADR 0002's drift contract: these
  tables are reconciled by hand against upstream, and the generated
  headers name the upstream commit.
- **Reopen a list the instant esc closes it** — rejected. `esc` is
  "leave me alone about this word," so a dismissal is sticky per span
  (`{source, start}`) until the trigger's start moves or a keystroke
  produces a different match; typing further into the same span (which
  only moves the end) stays quiet.
- **Send a typing notice on every navigation key** — rejected. Cycling
  and dismissing do not change the line, so they send nothing; only
  accept does, the same notice ordinary typing would have sent for that
  keystroke.
- **Clip or scroll the transcript viewport under an unbounded list** —
  rejected. `layout` reserves the list's rows (a header plus up to 5)
  from the viewport before it draws, so the screen is always exactly
  `height` lines; below 3 spare rows the list does not draw at all
  (no-list mode), and cycling instead previews the highlighted
  candidate directly in the line, trailing space trimmed until accept
  adds it back. Same spirit as the splash bailing on a small window
  (ADR 0005).
- **Let a completion push the line past CharLimit** — rejected in two
  forms. Accepting silently (`textinput.SetValue` truncates the *end*
  of the result on its own) throws away characters the user typed
  rather than the completion that caused the overflow — invisible data
  loss. Truncating the *insert* instead (offering `@ali` for `@alice`)
  hands back a name the source never listed, which a mention or a
  command can no longer parse. Refusing the splice outright and saying
  why is the only option that loses nothing: the list closes without
  the span being dismissed, since backspacing inside the word to make
  room does not move the span's start, so a plain dismiss would leave
  it unreachable until a whole new trigger was typed.

## Consequences

- A source is small and self-contained: `Name`, `Match`, `Candidates`.
  Phases 1-4 (mentions, commands, emotes, emoji) each add one without
  touching the engine, the UI's key handling, or the layout math.
- Candidate labels can carry network text (usernames, later on) once a
  source reads the roster, so the row renderer runs every `Label` and
  `Detail` through `render.Line` before drawing, same as every other
  network string that reaches the screen.
- The highlight uses Reverse plus color, like the status bar and the
  mention highlight, so it still reads as a bar under `NO_COLOR`; a
  Dim or Detail row is stripped of its own faint sub-styling first, so
  the whole row is one styled run rather than a bar broken by an
  unstyled gap where the sub-style's reset fired.
- The command table, the emote list and the emoji catalog become three
  more things reconciled by hand against the upstream `ngchat` repo,
  alongside the protocol sync ADR 0002 already documents.
- Embedded lists (emotes in phase 3, emoji in phase 4) extend the same
  drift contract rather than opening a new decision: `go generate`
  reads `$NGCHAT_UPSTREAM`, writes committed `_gen.go` files, and fails
  loudly if the checkout is missing rather than silently skipping.
