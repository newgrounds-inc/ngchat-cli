package complete

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// emojiTrigger is upstream's emoji regex
// (strategies/emoji_strategy.tsx's match field), ported unchanged. \B
// keeps a non-word boundary immediately before the ":" so "http://x"
// (":" right after the word character "p") never opens a list, while a
// ":" at line start or after a space does. The two-character minimum
// after the colon (one required class character, one or more of the
// rest) is what keeps ":)", ":D", ":P", ":3", ":/" and ":-)" typeable:
// none of them has a second character in the trigger's class run
// starting right after the colon. The whole match starts at "\B", the
// same byte offset as the literal ":" that follows it, since \B is
// zero-width; Match uses that to report the ":" as the span's start.
var emojiTrigger = regexp.MustCompile(`(?i)\B:([+0-9a-z][-+_*0-9a-z]+)$`)

// emoji is one embedded catalog row: a shortname and its glyph,
// decoded from upstream's codepoints at generate time (emoji_gen.go)
// so nothing in this package ever parses hex.
type emoji struct {
	Name, Glyph string
}

// Emoji completes emoji shortnames from emojiCatalog, the embedded
// list internal/complete/gen writes from the upstream checkout. Like
// Emotes it has no fields: the catalog is static between builds.
type Emoji struct{}

// Name identifies the emoji source for Engine's sticky dismiss.
func (Emoji) Name() string { return "emoji" }

// Match reports the span of an open ":shortname" token, per
// emojiTrigger. start is the byte offset of the ":", not the first
// character of term.
func (Emoji) Match(line string, cursor int) (start int, term string, ok bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	head := line[:cursor]
	loc := emojiTrigger.FindStringSubmatchIndex(head)
	if loc == nil {
		return 0, "", false
	}
	return loc[0], head[loc[2]:loc[3]], true
}

// Candidates ranks emojiCatalog by shortname against term, stripping a
// "*" first the same way Emotes does: the shared three-tier Rank
// already treats a gap as "anything" at its subsequence tier, so
// stripping the wildcard reproduces upstream's fuzzysort behavior
// without a wildcard implementation of its own. Insert always ends
// with ": " (the delimiter and the trailing space the wire format
// wants); Label pairs a fixed-width glyph column with the shortname so
// every row's name starts at the same column regardless of how wide
// the glyph itself renders.
func (Emoji) Candidates(term string) []Candidate {
	term = strings.ReplaceAll(term, "*", "")
	ranked := Rank(term, emojiCatalog, func(e emoji) string { return e.Name })
	// Engine.Complete already drops everything past MaxCandidates, but
	// only after every one of ranked's matches has had a Candidate
	// (glyphCell padding included) built for it: a term like "an",
	// which the subsequence tier alone matches against a few thousand
	// of the catalog's ~4000 shortnames, would otherwise build and
	// immediately discard almost all of that work on every keystroke.
	// Truncating here first turns that into the cost of Rank alone.
	if len(ranked) > MaxCandidates {
		ranked = ranked[:MaxCandidates]
	}
	out := make([]Candidate, len(ranked))
	for i, e := range ranked {
		out[i] = Candidate{
			Insert: ":" + e.Name + ": ",
			Label:  glyphCell(e.Glyph) + ":" + e.Name + ":",
		}
	}
	return out
}

// glyphCell pads g to a fixed 3-cell display column so the shortname
// that follows lines up under narrow and double-width glyphs alike:
// a 2-cell glyph gets one trailing space, a 1-cell glyph gets two.
// Width is measured with ansi.StringWidth rather than len or
// utf8.RuneCountInString because a glyph can be a ZWJ sequence with
// variation selectors (a family or couple emoji, say) whose byte and
// rune counts have nothing to do with how many terminal columns it
// occupies.
//
// ansi.StringWidth under-reports every skin-tone modifier sequence in
// the catalog (":woman_lifting_weights_tone1:" and 84 others like it)
// as 1 cell; UTS #51 §1.4.4 makes emoji presentation, and so 2 cells,
// mandatory for any sequence carrying a modifier, regardless of what
// the base glyph would render as alone. hasEmojiModifier raises the
// measured width to at least 2 for those before padding, which is the
// one case this function corrects for. A handful of unrelated glyphs
// (":detective:", ":hand_splayed:", ":person_bouncing_ball:",
// ":person_golfing:", ":person_lifting_weights:",
// ":transgender_symbol:") also measure narrower than they render in
// some terminals; nothing here is wrong about those specifically, so
// they are left alone rather than special-cased like the modifier
// sequences — see docs/internals/complete.md and the manual test plan
// for what to eyeball on a new terminal.
func glyphCell(g string) string {
	w := ansi.StringWidth(g)
	if w < 2 && hasEmojiModifier(g) {
		w = 2
	}
	if w < 3 {
		return g + strings.Repeat(" ", 3-w)
	}
	return g + " "
}

// emojiModifierStart and emojiModifierEnd bound the five Fitzpatrick
// skin-tone modifier codepoints (U+1F3FB..U+1F3FF), the range
// hasEmojiModifier checks glyphCell's glyphs against.
const (
	emojiModifierStart = 0x1f3fb
	emojiModifierEnd   = 0x1f3ff
)

// hasEmojiModifier reports whether g contains a skin-tone modifier
// codepoint, the one case glyphCell corrects ansi.StringWidth's
// measurement for.
func hasEmojiModifier(g string) bool {
	for _, r := range g {
		if r >= emojiModifierStart && r <= emojiModifierEnd {
			return true
		}
	}
	return false
}
