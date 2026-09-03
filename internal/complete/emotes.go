package complete

import (
	"regexp"
	"strings"
)

// emoteTrigger is upstream's dank-meme regex
// (strategies/dank_meme_strategy.tsx), ported with one necessary
// change: RE2 has no lookbehind, so the leading non-capturing
// `(?:^|\s)` stands in for upstream's `(?<= |^)`. Both prefixes need a
// trailing character or two before firing so ordinary words survive:
// "tf" needs two ("tf", "tfw" stay typeable), "ng?" needs one ("ngl"
// stays typeable, "ngaK" still completes). The whole match, not a
// capture group, is the term: the group only carries the byte offset
// where it starts, which is what a leading "\s" would otherwise shift.
var emoteTrigger = regexp.MustCompile(`(?i)(?:^|\s)(ng[a-z*][0-9a-z*]+|tf[0-9a-z*]{2,})$`)

// Emotes completes NG emote ("dank meme") shortcodes from emoteCodes,
// the embedded list internal/complete/gen writes from the upstream
// checkout. Unlike Mentions and Commands it has no fields: the list is
// static between builds, so there is nothing to read fresh per query.
type Emotes struct{}

// Name identifies the emote source for Engine's sticky dismiss.
func (Emotes) Name() string { return "emote" }

// Match reports the span of an open ng…/tf… token, per emoteTrigger.
// term is the whole token (wildcards included); Candidates strips them
// before ranking.
func (Emotes) Match(line string, cursor int) (start int, term string, ok bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	head := line[:cursor]
	loc := emoteTrigger.FindStringSubmatchIndex(head)
	if loc == nil {
		return 0, "", false
	}
	return loc[2], head[loc[2]:loc[3]], true
}

// Candidates ranks emoteCodes against term with a "*" in term treated
// as a gap of any length: the shared three-tier Rank already lets its
// subsequence tier match a gap, and upstream's fuzzysort (which this
// engine replaces per ADR 0006) has no wildcard semantics of its own
// either, so stripping the "*" before ranking reproduces the same
// result without inventing one. A code with no match at any tier is
// dropped, and Engine never opens a list with none left.
func (Emotes) Candidates(term string) []Candidate {
	term = strings.ReplaceAll(term, "*", "")
	ranked := Rank(term, emoteCodes, identity)
	out := make([]Candidate, len(ranked))
	for i, code := range ranked {
		out[i] = Candidate{Insert: code + " ", Label: code}
	}
	return out
}

// identity is Rank's key function for a list that is already the
// string to compare, needed because Rank is generic over the item type
// and emoteCodes is []string rather than a struct with a name field.
func identity(s string) string { return s }
