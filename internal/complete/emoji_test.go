package complete

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestEmojiMatch ports the trigger edge cases from upstream
// regexes.test.ts's emoji strategy block (the same regex as
// strategies/emoji_strategy.tsx's match field), plus "a:smile" to
// check \B explicitly and a multi-word line to check the byte offset
// lands on the ":", not the word after it. cursor is -1 for every case
// that means "the end of line" (the common case); the one mid-token
// case sets it explicitly to check that only line[:cursor] is
// consulted, per Match's contract.
func TestEmojiMatch(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		cursor    int
		wantStart int
		wantTerm  string
		wantOK    bool
	}{
		{"two chars after colon", ":smi", -1, 0, "smi", true},
		{"mid-line, start is the colon", "hi :smile", -1, 3, "smile", true},
		{"plus-prefixed name", ":+1", -1, 0, "+1", true},
		{"bare colon", ":", -1, 0, "", false},
		{"smiley face", ":)", -1, 0, "", false},
		{"frowny face", ":(", -1, 0, "", false},
		{"shrug-ish D", ":D", -1, 0, "", false},
		{"tongue-out P", ":P", -1, 0, "", false},
		{"digit-only shortcut", ":3", -1, 0, "", false},
		{"slash", ":/", -1, 0, "", false},
		{"dash smiley", ":-)", -1, 0, "", false},
		{"dash frowny", ":-(", -1, 0, "", false},
		{"bare url scheme", "http://", -1, 0, "", false},
		{"url with path", "https://x", -1, 0, "", false},
		{"after a space", "see :smile", -1, 4, "smile", true},
		{"no boundary before colon", "a:smile", -1, 0, "", false},
		{"cursor mid-token", ":smile", 3, 0, "sm", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cursor := tc.cursor
			if cursor < 0 {
				cursor = len(tc.line)
			}
			e := Emoji{}
			start, term, ok := e.Match(tc.line, cursor)
			if ok != tc.wantOK {
				t.Fatalf("Match(%q, %d) ok = %v, want %v", tc.line, cursor, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if start != tc.wantStart || term != tc.wantTerm {
				t.Errorf("Match(%q, %d) = (%d, %q), want (%d, %q)",
					tc.line, cursor, start, term, tc.wantStart, tc.wantTerm)
			}
		})
	}
}

// TestEmojiCandidatesRanksPrefixFirst: term "smile" ranks the exact
// prefix match ":smile:" ahead of the substring match ":sweat_smile:"
// — a prefix match beats a substring match under the shared three-tier
// Rank, the same rule Emotes uses. The term is the full "smile" rather
// than a shorter prefix like "sm" or "smi": those match enough of the
// catalog's own "sm*"/"smi*" prefix family (small_blue_diamond,
// smiley, smirk, ...) to fill Candidates' MaxCandidates cap before the
// ranking ever reaches a substring-tier item, which would push
// "sweat_smile" out of the result before this test got to compare the
// two tiers against it.
func TestEmojiCandidatesRanksPrefixFirst(t *testing.T) {
	e := Emoji{}
	got := e.Candidates("smile")
	idx := make(map[string]int, len(got))
	for i, c := range got {
		idx[c.Insert] = i
	}
	smile, hasSmile := idx[":smile: "]
	sweat, hasSweat := idx[":sweat_smile: "]
	if !hasSmile || !hasSweat {
		t.Fatalf("Candidates(\"smile\") = %v, want both :smile: and :sweat_smile:", got)
	}
	if smile >= sweat {
		t.Errorf("Candidates(\"smile\"): :smile: at %d, :sweat_smile: at %d, want :smile: first",
			smile, sweat)
	}
}

// TestEmojiCandidatesWildcardStripped: "*" in the term is removed
// before ranking, the same wildcard handling Emotes uses, so
// "*mile" still finds "smile" via the subsequence tier over the
// stripped term "mile".
func TestEmojiCandidatesWildcardStripped(t *testing.T) {
	e := Emoji{}
	got := e.Candidates("*mile")
	for _, c := range got {
		if c.Insert == ":smile: " {
			return
		}
	}
	t.Errorf("Candidates(\"*mile\") = %v, want :smile: present", got)
}

// TestEmojiCandidatesDigitStartTerm covers the trigger class's
// digit-start allowance (":100" is as legal a token as ":+1"). The
// catalog this is generated against has no "+1" shortname to test the
// "+"-prefixed case literally (upstream's slim catalog drops it), so
// this exercises the same rule with "100", the other class member the
// spec names as a fallback; see the deviation note in the report.
func TestEmojiCandidatesDigitStartTerm(t *testing.T) {
	e := Emoji{}
	got := e.Candidates("100")
	if len(got) == 0 || got[0].Insert != ":100: " {
		t.Fatalf("Candidates(\"100\")[0] = %+v, want Insert \":100: \"", got)
	}
}

// TestEmojiCandidatesEmptyForNoMatch mirrors Emotes: a term matching
// nothing returns no candidates, which is what Engine.Complete relies
// on to keep the list from opening at all.
func TestEmojiCandidatesEmptyForNoMatch(t *testing.T) {
	e := Emoji{}
	if got := e.Candidates("zzzzzznotanemoji"); len(got) != 0 {
		t.Errorf("Candidates(\"zzzzzznotanemoji\") = %v, want none", got)
	}
}

// TestEmojiCandidatesInsert checks the exact replacement text: the
// shortname wrapped in colons with a trailing space, matching the web
// client's own delimiter.
func TestEmojiCandidatesInsert(t *testing.T) {
	e := Emoji{}
	got := e.Candidates("smile")
	if len(got) == 0 || got[0].Insert != ":smile: " {
		t.Fatalf("Candidates(\"smile\")[0] = %+v, want Insert \":smile: \"", got)
	}
}

// TestGlyphCellAlignsShortnameColumn checks the label's display width
// up to the first ":" is always 3, for three glyphs ansi.StringWidth
// measures at the ordinary 2 cells ("100", "smile") and a ZWJ family
// sequence ("family_man_boy") it also measures at 2 despite its four
// codepoints, so the shortname column lines up regardless of how many
// codepoints the glyph itself is made of. TestGlyphCellPadding covers
// glyphCell's other branches directly: a glyph narrower than 2 cells,
// and one ansi.StringWidth already measures at 3 or more.
func TestGlyphCellAlignsShortnameColumn(t *testing.T) {
	e := Emoji{}
	for _, name := range []string{"100", "smile", "family_man_boy"} {
		t.Run(name, func(t *testing.T) {
			got := e.Candidates(name)
			var label string
			found := false
			for _, c := range got {
				if c.Insert == ":"+name+": " {
					label = c.Label
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("Candidates(%q) has no exact match, want one", name)
			}
			idx := -1
			for i, r := range label {
				if r == ':' {
					idx = i
					break
				}
			}
			if idx < 0 {
				t.Fatalf("Label %q has no \":\"", label)
			}
			if w := ansi.StringWidth(label[:idx]); w != 3 {
				t.Errorf("ansi.StringWidth(%q) = %d, want 3", label[:idx], w)
			}
		})
	}
}

// TestGlyphCellPadding exercises glyphCell directly against the three
// shapes its branches and its skin-tone correction exist for:
//   - a glyph ansi.StringWidth measures at 3 cells or more gets
//     exactly one trailing space (the ">= 3" branch), the same as an
//     ordinary 2-cell glyph, rather than being padded out further;
//   - ":detective:" (U+1F575), one of the handful of glyphs that
//     render narrower than expected in some terminals for reasons
//     outside glyphCell's control (see its doc comment) — measured at
//     1 cell here, it gets two trailing spaces, left uncorrected;
//   - ":person_lifting_weights_tone1:" (U+1F3CB U+1F3FB), a skin-tone
//     modifier sequence ansi.StringWidth under-reports as 1 cell,
//     which hasEmojiModifier raises to 2 before padding, so it gets
//     one trailing space rather than the two an uncorrected width-1
//     reading would produce.
func TestGlyphCellPadding(t *testing.T) {
	tests := []struct {
		name  string
		glyph string
		want  string
	}{
		{"width >= 3 gets one space", "abc", "abc "},
		{"narrow glyph left uncorrected gets two spaces",
			"\U0001f575", "\U0001f575  "},
		{"skin-tone modifier corrected to 2 cells gets one space",
			"\U0001f3cb\U0001f3fb", "\U0001f3cb\U0001f3fb "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := glyphCell(tc.glyph); got != tc.want {
				t.Errorf("glyphCell(%q) = %q, want %q", tc.glyph, got, tc.want)
			}
		})
	}
}

// TestEmojiCatalogEmbedded checks the generated data's shape rather
// than its exact contents, which would make the test brittle against
// a routine regeneration: a floor on the count, "smile" present, no
// duplicate names, every glyph non-empty, and sorted by Name in plain
// string order (the generator's own rule; unlike emoteCodes this is
// not case-folded, since every real shortname is already lowercase).
func TestEmojiCatalogEmbedded(t *testing.T) {
	if len(emojiCatalog) < 3000 {
		t.Fatalf("len(emojiCatalog) = %d, want >= 3000", len(emojiCatalog))
	}
	var hasSmile bool
	seen := make(map[string]bool, len(emojiCatalog))
	for i, e := range emojiCatalog {
		if e.Name == "smile" {
			hasSmile = true
		}
		if e.Glyph == "" {
			t.Errorf("emojiCatalog[%d] (%q) has an empty Glyph", i, e.Name)
		}
		if seen[e.Name] {
			t.Errorf("emojiCatalog has a duplicate name: %q", e.Name)
		}
		seen[e.Name] = true
		if i > 0 && emojiCatalog[i-1].Name > e.Name {
			t.Errorf("emojiCatalog not sorted at %d: %q before %q",
				i, emojiCatalog[i-1].Name, e.Name)
		}
	}
	if !hasSmile {
		t.Error("emojiCatalog missing \"smile\"")
	}
}

// TestEmojiDismissStaysQuietUntilSpanMoves runs the sticky-dismiss
// contract through the real Engine, the same shape as
// emotes_test.go's coverage: esc on an open emoji list keeps quiet
// through further typing in the same word, and a new word (a new span
// start) opens again.
func TestEmojiDismissStaysQuietUntilSpanMoves(t *testing.T) {
	var e Engine
	sources := []Source{Emoji{}}

	res, ok := e.Complete(":smi", 4, sources)
	if !ok {
		t.Fatal("Complete(\":smi\") = not ok, want a match")
	}
	e.Dismiss(res)

	if _, ok := e.Complete(":smil", 5, sources); ok {
		t.Error("Complete(\":smil\") after Dismiss = ok, want it to stay closed")
	}

	if _, ok := e.Complete(" :smi", 5, sources); !ok {
		t.Error("Complete(\" :smi\") (a new span start) = not ok, want it to open")
	}
}
