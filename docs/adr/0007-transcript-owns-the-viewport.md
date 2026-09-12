# The transcript owns its viewport and its row styles

The chat log lived in the Bubble Tea root model as seven fields and a
handful of methods (`push`, `locate`, `refresh`, `renderItem`). Its one
real invariant, that the reader's place is a row and not a screen
line, so a reveal, a timestamp toggle or a resize puts the same row
back under the top of the screen, was enforced by an ordering rule:
read the anchor *before* mutating, then refresh with it. Five call
sites each had to remember that, and the last two `fix(ui)` commits
before this change were both places one had not. Every push also
re-rendered all 2000 rows.

`internal/transcript` now holds the rows, the rendered-line table, the
anchor and the `viewport.Model`. Callers append, resize, set options,
page and follow; `Following()`, `Rows()`, `Height()` and `View()` are
what they read back. The transcript builds its own row styles from the
theme, the way `splash.Palette` does (ADR 0005), so the UI's style set
shrinks to the chrome.

## Considered Options

- **Keep the viewport in the model, hand the transcript the offset**
  — rejected. The transcript would need the current offset before each
  mutation, so "locate first" survives, just at a different seam. The
  viewport inside is what makes the ordering an internal invariant.
- **A file in `internal/ui` instead of a package** — rejected. The test
  surface would still be the whole `Model` (sized through
  `tea.WindowSizeMsg`, keys through `Update`); a package gives the
  anchoring tests a transcript and nothing else, and `go test
  ./internal/transcript` runs in a tenth of a second.
- **One style set shared with the UI** — rejected. The package would
  then import the UI's unexported styles or the UI would export them;
  either way the row roles (user, mod, self, mention, DM, event,
  spoiler, error, time) are the transcript's and no one else's. ADR
  0005's "every style built once from a theme" holds per package, as
  it already did for splash.
- **A zero-time fallback to `time.Now` inside the transcript** —
  rejected. The caller always stamps; a module that never reads the
  clock is testable for `ctrl+t` output with fixed times.
- **Keep `kind` a string** — rejected; a typed `Kind` lets the drawing
  switch be checked and the classification tested by value.

## Consequences

- Anchoring has one home: `locate`/`show` in the transcript, tested
  through `Append`/`SetOptions`/`Resize`/`PageUp` without Bubble Tea.
- One behaviour change rides along: the old `layout` read the anchor
  *after* resizing the viewport, so a terminal that shrank while the
  reader was at the bottom left them scrolled up under the "more
  messages below" banner. `Resize` reads it first, so they stay at the
  bottom (`TestShrinkKeepsFollowing`).
- A push renders one row. The viewport's own `SetContentLines` still
  walks every line for its width bookkeeping, so the cap stays.
- The `/who` listing is a `Roster` row drawn as given, because it
  carries its own dimming and must not gain the event style; the
  "no user list yet" answer is an event row.
- Event rows now carry plain text and the transcript styles them, so
  the model builds them from sanitized strings and never from a style.
- `docs/internals/transcript.md` carries the invariants that used to
  sit in `ui.md`; `CONTEXT.md` names Transcript, Row, Anchor and
  Following.
