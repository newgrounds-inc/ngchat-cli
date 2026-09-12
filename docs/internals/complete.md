# `internal/complete` — the composer's tab-completion engine

Pure, like `theme` and `splash`: no Bubble Tea, no lipgloss. See ADR
0006 and `docs/plans/tab-completion.md`; vocabulary (trigger, span,
candidate, source, dismiss) is defined in `CONTEXT.md`. How the UI
drives the list (keys, no-list mode, resize) is in `ui.md`.

## Engine

A `Source` is a trigger plus the candidate list behind it; the four
sources are phases 1-4 below. `Engine.Complete` tries sources in order
and the first `Match` wins, so an earlier source's trigger cannot be
shadowed by a later one added down the line.

Offsets are byte offsets throughout (`Match`'s span,
`Result.Start`/`End`), since Go's regexp is byte-native; only the UI
converts to and from the input widget's rune positions.

A list never opens with zero candidates. `Dismiss` tracks one
sticky-closed span keyed by `{source, start}` so `esc` stays quiet
through further typing in the same word but clears the moment the
trigger moves or stops matching.

## Ranking

`Rank` is the in-house, dependency-free three-tier ranking (prefix,
then substring, then subsequence, alphabetical within a tier) every
source but `Commands` uses. Commands keep the web's own
prefix-plus-alias rule instead (ADR 0006), since that one was never
fuzzy upstream.

The command table, the emote list and the emoji catalog are hand- or
generator-reconciled against upstream; see `protocol.md`.

## Sources

**`Mentions` (phase 1)** ranks the live roster rather than a fixed
list, reading `Users`/`Self` fresh on every `Candidates` call, so a
join, a leave or an away change between keystrokes needs no change
notification into this package.

**`Commands` (phase 2)** triggers on the whole line rather than a
trailing word: `/` only opens the list as the first character typed.
It filters `CommandTable` to the viewer's rank, read fresh from
`Viewer` on every query so a `revalidated` rank change takes effect
without a hook into this package either. That filter is cosmetic, as
upstream documents: the server is the only enforcement point, and
hiding a command here only keeps the list from advertising a dead end.

**`Emotes` (phase 3)** has no fields, unlike `Mentions` and
`Commands`: `emoteCodes` is static between builds, so there is nothing
to read fresh. Its trigger needs whitespace or the line start
immediately before `ng`/`tf` (RE2 has no lookbehind, so a leading
`(?:^|\s)` stands in for upstream's `(?<= |^)`), which is what keeps
`@ngfoo` a mention rather than an emote. A `*` typed in the term is
stripped before ranking rather than given wildcard semantics of its
own: the shared `Rank`'s subsequence tier already treats the gap as
"anything", the same approximation upstream's fuzzysort gives it.

**`Emoji` (phase 4)** is the same shape as `Emotes`, over
`emojiCatalog` (`[]emoji{Name, Glyph}`) instead of `emoteCodes`. Its
trigger is `\B:([+0-9a-z][-+_*0-9a-z]+)$`, upstream's own regex, where
`\B` is what keeps `http://` closed and the two-character minimum is
what keeps `:)`/`:D`/`:-)` typeable.

## Emoji column alignment

Each emoji candidate's row pairs `glyphCell` (the glyph padded to a
fixed 3-cell column with `ansi.StringWidth`, which already accounts for
ZWJ sequences and variation selectors) with the shortname, so the
shortname starts at the same column for emoji-presentation glyphs, a
wide family/couple glyph and a narrow one alike.

`ansi.StringWidth` under-reports a skin-tone modifier sequence
(U+1F3FB..U+1F3FF, e.g. `:woman_lifting_weights_tone1:`) as 1 cell
where UTS #51 mandates emoji presentation (2 cells); `glyphCell`
corrects for that one known case. A handful of unrelated glyphs
(`:detective:` among them) still render narrower than the column
expects in some terminals for reasons outside this function's control.
Eyeball `:woman_lifting_weights_tone1:` and `:detective:` specifically
when checking alignment on a new terminal.
