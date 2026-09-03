# Tab completion in the composer

Status: agreed 2026-09-03
Owner: Brendon C.
Related: ADR 0006 (to write, phase 0), ADR 0002 (the drift contract
this extends), `newgrounds-inc/ngchat`
`src/client/app/chat/autocomplete/` (the reference behavior),
`src/client/app/chat/autocomplete/slash_commands.ts` (command table),
`src/lib/emoticons.json` + `src/lib/emoticons-small.json` (emotes),
`src/client/app/chat/autocomplete/strategies/emoji_catalog.generated.ts`
(emoji), `strategies/regexes.test.ts` (trigger edge cases to port).

## Checklist

### Phase 0: engine and list

- [ ] `internal/complete`: `Source` interface, `Complete(line,
      cursor, sources)` returning the trigger span and up to 10
      candidates, three-tier ranking, sticky dismiss keyed by
      `{source, spanStart}`.
- [ ] Table tests: trigger detection (port `regexes.test.ts`), span
      bounds at the cursor and mid-line, ranking order, cap, dismiss
      survives further typing in the same span and clears when the
      span changes.
- [ ] UI: `completion` state on `Model`; keys `tab`/`shift+tab`/
      `down`/`up` cycle with wrap, `enter` accepts, `esc` dismisses;
      any other key falls through to the input and recomputes.
- [ ] UI: list drawn above the input, header row with key hints, up
      to 5 candidate rows, the window scrolls with the highlight,
      `+N more` tail; `layout` reserves the rows from the viewport.
- [ ] UI: no-list mode when the viewport cannot give 3 rows: cycling
      writes the highlighted candidate into the span as a preview,
      accept adds the trailing space.
- [ ] UI: navigation keys send no typing notice; accept does.
- [ ] Help line gains `tab complete`.
- [ ] UI tests: open/cycle/accept/dismiss by key, viewport height
      with the list open and closed, no typing notice on `tab`, the
      no-list preview path, ANSI-stripped row text.
- [ ] ADR 0006, `CONTEXT.md` vocabulary (trigger, span, candidate,
      source, dismiss), `AGENTS.md` architecture paragraph.

### Phase 1: mentions

- [ ] `complete.Mentions` over the roster: trigger `\B@` with term
      `[a-zA-Z0-9-!*]*`, empty term allowed, self excluded, away users
      kept and dimmed, alphabetical when the term is empty, replacement
      `@name ` with the trailing space.
- [ ] Roster feed: the source reads the model's `users` map at query
      time so joins and leaves are reflected without a hook.
- [ ] Tests: self excluded, away dimmed, bare `@` sorted like `/who`,
      `\B` refuses `foo@bar`, `@!` typed by hand is left alone.
- [ ] Docs: README key list, `docs/manual-test-plan.md` §5 rows,
      `CHANGELOG.md`.

### Phase 2: commands

- [ ] `complete.Commands`: hand-ported table of the web's 31 commands
      plus `/who` (name, aliases, access, description), trigger
      `^/[a-z0-9/*]*$` on the whole line before the cursor, prefix match
      with `*` wildcard against name and aliases, alphabetical,
      replacement `/name ` (canonical name even when an alias matched).
- [ ] Model keeps `isAdmin`/`isChatMod` from `authenticated`, refreshed
      on `revalidated`; commands above the viewer's rank are hidden.
- [ ] Row shows `/kick (k)` and the description dimmed.
- [ ] Tests: access filter per rank, rank change on `revalidated`,
      alias match inserts the canonical name, wildcard, `/` alone
      lists everything the viewer may use, `/x y` mid-line does not
      trigger.
- [ ] Sync note in `AGENTS.md` next to the protocol one: the table is
      reconciled by hand against upstream `slash_commands.ts`.
- [ ] Docs: README, manual test plan, CHANGELOG.

### Phase 3: emotes (NG "dank memes")

- [ ] `internal/complete/gen`: `go generate` reads the upstream
      checkout (`NGCHAT_UPSTREAM`, no default) and writes
      `emotes_gen.go` from the two JSON lists plus `ngrandom`, header
      carries the upstream commit.
- [ ] `complete.Emotes`: trigger `(?:^|\s)(ng[a-z*][0-9a-z*]+|tf[0-9a-z*]{2,})$`
      (RE2 has no lookbehind, the leading group replaces it), the
      whole token is the term, three-tier ranking, replacement
      `shortcode ` with no delimiters, list opens only when there is
      at least one match.
- [ ] Tests: `ngl` does not trigger, `ngle` does, `tfw` does not,
      `ngrandom` present, sticky dismiss on an ordinary word.
- [ ] Docs: README, manual test plan, CHANGELOG, `AGENTS.md` sync
      note extended to the emote list.

### Phase 4: emoji

- [ ] `gen` also writes `emoji_gen.go` (shortname, codepoints) from
      the upstream generated catalog.
- [ ] `complete.Emoji`: trigger `\B:([+0-9a-z][-+_*0-9a-z]+)$`, three-tier
      ranking over shortnames, row shows the glyph then `:smile:`,
      replacement `:smile: `.
- [ ] Glyph column: decode codepoints once at generate time into the
      Go string, pad rows by display width so shortnames align under
      wide and double-width glyphs.
- [ ] Tests: `:)` and `:D` never trigger, `http://` never triggers,
      `:sm` ranks `:smile:` above `:sweat_smile:`, glyph row width.
- [ ] Docs: README, manual test plan, CHANGELOG, sync note extended.

### Wrap

- [ ] Hand-test on the terminal matrix (`docs/manual-test-plan.md`):
      the list under kitty/Ghostty/Terminal.app/Windows Terminal, a
      40-column window, a 10-row window (no-list mode), `NO_COLOR`,
      tmux.

## Where things stood on 2026-09-03

`tab` reached the textinput widget unbound, where bubbles' own
suggestion feature was inert (never fed, and it matches the whole
line, not the word at the cursor). The roster already lived on the UI
model keyed by user ID with username, admin/mod flags and away state.
Mentions, commands, emotes and emoji all go out as plain text in the
one `message` frame, and nothing in the protocol carries any list to
complete from. The web client keeps every list as bundled data or a
separate HTTP endpoint, and today only `@` and `/` show its inline
dropdown; `ng`/`tf` and `:` open a toolbar picker instead. The CLI's
list is therefore a better experience than the web has for emotes
and emoji, not a copy.

## Decisions

- **One engine, four sources, mentions first.** `internal/complete`
  is a pure package: input is the line and the cursor offset, output
  is the span to replace and the candidates. Each source implements
  one small interface. The UI wires the roster and the privilege
  flags in and draws the result. Same split as `render`, `theme`,
  `splash`.
- **A list, not inline cycling.** Candidates are drawn above the
  input, the viewport gives up the rows while the list is open.
  Emotes and commands are things people do not know the full name of,
  so seeing the alternatives is the point. Cap 10 candidates, show 5,
  the window scrolls with the highlight.
- **Web semantics, terminal keys.** What lands in the line (the
  trigger rules, the replacement text, the trailing space) matches
  the web client so muscle memory carries over. `tab`/`shift+tab`
  and the arrows cycle with wrap, `enter` accepts, `esc` dismisses.
  `enter` only sends when no list is open. A single candidate is
  accepted by `tab` outright.
- **Trigger characters only.** `@`, `/`, `:` and the `ng`/`tf`
  emote prefixes, exactly as the web fires them. No bare-word nick
  completion; it can be added as a fifth source if missed.
- **Word at the cursor.** The span runs from the trigger back to the
  nearest whitespace and forward to the cursor, so editing mid-line
  completes there.
- **Ranking is in-house and deterministic**, a named departure from
  the web's fuzzysort: case-insensitive prefix matches first, then
  substring, then subsequence, alphabetical within each tier. Commands
  keep the web's exact rule instead (prefix with `*` wildcard, name
  and aliases, alphabetical) since that one is not fuzzy upstream.
  No new dependency.
- **Self excluded, away users dimmed.** The web shows the raw roster;
  excluding yourself is the one deliberate deviation in the mention
  candidates. Group mentions (`@!name`) are not completed because
  nothing lists them; typed by hand they pass through untouched.
- **Privilege filtering is cosmetic.** Commands above the viewer's
  rank are hidden from the list from the `authenticated` flags,
  refreshed on `revalidated`. The server is the only enforcement
  point, as upstream documents.
- **Lists are embedded, not fetched.** Emote shortcodes and emoji
  shortnames are generated into Go source from the upstream checkout
  by `go generate` and committed. Startup fetching would add latency
  and a failure mode for data that changes a few times a year. This
  extends ADR 0002's drift contract: the command table, the emote
  list and the emoji catalog are reconciled by hand against upstream,
  and the generated headers carry the upstream commit.
- **Sticky dismiss per span.** `esc` on a span keeps that span quiet
  until the trigger or its start moves, so an emote list that opened
  on an ordinary `ng…` word does not reopen on every keystroke. The
  list also never opens with zero matches.
- **Silent navigation.** Cycling and dismissing send no typing
  notice; accepting changes the line and sends one as usual.
- **Small terminals still complete.** With fewer than 3 rows to
  spare the list is not drawn; cycling previews the highlighted
  candidate in the span instead, and accept adds the trailing space.
  Same spirit as the splash bailing on a small window.
- **Out of scope, listed as future sources:** semantic emote search
  (`eme`, a rate-limited site endpoint), giphy, youtube, soundboard.

## Phase notes

### Engine shape

```go
type Candidate struct {
    Insert  string // full replacement for the span, trailing space included
    Label   string // what the row shows, e.g. "/kick (k)"
    Detail  string // dimmed suffix, e.g. a command description
    Dim     bool   // away user
}

type Source interface {
    // Match reports the span [start, cursor) this source owns in
    // line, or ok=false. Term is what the source ranks against.
    Match(line string, cursor int) (start int, term string, ok bool)
    Candidates(term string) []Candidate
}
```

Sources are tried in order emotes, emoji, mentions, commands; the
first `Match` wins, as upstream's engine does. The four triggers do
not overlap in practice (`@ngfoo` is a mention because the emote
trigger needs whitespace before it; `/` only fires as the whole
line), the order only matters for a future source.

The engine never touches the widget. The UI applies an accept by
splicing `Insert` over the span with `SetValue` and moving the cursor
to the end of the inserted text.

### Layout

`layout` today: status 1, viewport `h-3`, input 1, help 1. With a
list of `n` candidate rows plus one header row the viewport is
`h-4-n`, where `n = min(5, len(candidates), h-3-3)`; when that gives
`n < 1` the list is hidden and no-list mode is on. The header row
reads `tab/shift+tab next · enter accept · esc close`. The viewport
keeps its own scroll offset across the resize.

### Data generation

`internal/complete/gen/main.go`, run by a `//go:generate` directive
in the package. It reads `$NGCHAT_UPSTREAM/src/lib/emoticons.json`
and `emoticons-small.json` (plain arrays of shortcodes), appends
`ngrandom`, and parses the `[shortname, codepoints]` pairs out of
`emoji_catalog.generated.ts`. Output is two `_gen.go` files with
sorted Go slices and a header naming the upstream commit
(`git -C $NGCHAT_UPSTREAM rev-parse --short HEAD`). Missing
`NGCHAT_UPSTREAM` is an error, never a silent default. Tests do not
run the generator; they assert on the committed data (count above a
floor, `ngrandom` present, every emoji shortname wrapped in colons).

### Trigger regexes, ported

| Source   | Web                                                        | Go (RE2)                                              |
|----------|------------------------------------------------------------|-------------------------------------------------------|
| mention  | `\B@([a-zA-Z0-9-!*]*)$`                                     | same                                                  |
| command  | `^\/([a-z0-9/*]*)$`, whole prefix                           | same, `(?i)`                                          |
| emoji    | `\B:([+0-9a-z][-+_*0-9a-z]+)$`                              | same, `(?i)`                                          |
| emote    | `(?<= \|^)(?:ng[a-z*][0-9a-z*]+\|tf[0-9a-z*]{2,})$`          | `(?:^\|\s)(ng[a-z*][0-9a-z*]+\|tf[0-9a-z*]{2,})$`, `(?i)` |

All run against the line up to the cursor. The mention `\B` is what
keeps `foo@bar` from opening a list; the emoji rule's two-character
minimum is what keeps `:)` and `:D` typeable.

## Later, when wanted

- **Bare-word nick completion** at line start, irssi style, as a
  fifth source with the lowest priority.
- **Semantic emote search** (`eme`): needs the site's search endpoint,
  a 250 ms debounce, and 429 handling in the list ("rate limited").
- **Soundboard** (`sfx`, `/sfx`): catalog in upstream
  `src/shared/soundboard.ts`, same generate-and-embed path.
- **Giphy and youtube** triggers: both are network lookups that
  insert URLs; only worth it if the terminal can preview them.
- **Emote descriptions in rows**: the semantic search index has a
  `conveys` field per emote; if it is ever exposed as static data the
  emote rows could show it dimmed like command descriptions.
