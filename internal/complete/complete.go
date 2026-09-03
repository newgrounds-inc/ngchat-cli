// Package complete is the pure completion engine behind the composer's
// tab-triggered lists (mentions, commands, emotes, emoji). It knows
// nothing about Bubble Tea, lipgloss or the theme: given a line and the
// cursor's byte offset it decides whether a trigger is open and which
// candidates to offer, and the UI does the drawing and the splicing.
// Modeled on internal/splash and internal/theme, which split a pure
// seam from the widget that draws it.
//
// Offsets are byte offsets throughout, not rune offsets: Go's regexp
// package (and every Source built on it) is byte-native, and the UI is
// the one place that has to convert to and from the textinput widget's
// rune positions.
package complete

// Candidate is one row a Source offers for the open trigger.
type Candidate struct {
	// Insert is the full replacement for the span, trailing space
	// included where the wire format wants one (e.g. "@alice ").
	Insert string
	// Label is what the row shows, e.g. "/kick (k)".
	Label string
	// Detail is a dimmed suffix, e.g. a command's description. May be
	// "", in which case the row shows no detail at all.
	Detail string
	// Dim marks a candidate that should draw faint, e.g. an away user.
	Dim bool
}

// Source is one trigger and the candidate list it offers once its
// trigger has fired. A source is stateless between calls: the engine
// re-queries it on every keystroke rather than caching, since the
// backing data (the roster, the privilege flags) can change under it.
type Source interface {
	// Name is a stable id for the source, used as half of the
	// sticky-dismiss key. It must not change across calls.
	Name() string
	// Match reports the span [start, cursor) in line that this source
	// owns, or ok=false if its trigger has not fired. Only
	// line[:cursor] is consulted — text after the cursor is never
	// part of a trigger. term is what Candidates ranks against; it is
	// usually, but need not be, line[start:cursor].
	Match(line string, cursor int) (start int, term string, ok bool)
	// Candidates ranks the source's list against term, in the order
	// the list should display them. May return nil when nothing
	// matches; a nil or empty result means the list does not open.
	Candidates(term string) []Candidate
}

// MaxCandidates caps how many candidates a Result carries. The UI shows
// at most 5 at once and scrolls the rest into view, so 10 is room for
// two screenfuls without the list needing to re-query as the user
// scrolls past what a slower source (a future network lookup) already
// returned.
const MaxCandidates = 10

// Result is one open completion: which source won, the span of the
// line it owns, and the candidates to show for it.
type Result struct {
	// Source is the winning Source's Name.
	Source string
	// Start and End are byte offsets into the line the completion
	// replaces on accept. End is always the cursor offset passed to
	// Complete, not necessarily line's length.
	Start, End int
	// Candidates is 1..MaxCandidates, in display order. Complete never
	// returns a Result with zero candidates — see its doc comment.
	Candidates []Candidate
}

// dismissKey identifies a span a user has dismissed: the source that
// owned it and where it started. The end is deliberately not part of
// the key, so typing more into the same span (which moves the end but
// not the start) keeps it dismissed.
type dismissKey struct {
	source string
	start  int
}

// Engine holds the one piece of state completion needs across
// keystrokes: the span, if any, the user closed with esc. Its zero
// value is ready to use.
type Engine struct {
	dismissed    dismissKey
	dismissedSet bool
}

// Complete tries sources in order and asks the first one whose trigger
// matches for its candidates — first Match wins, the same rule
// upstream's engine uses, so a future source's trigger cannot shadow an
// earlier one by accident. It reports ok=false when no source matches,
// when the winning span is the one Dismiss last closed (and the trigger
// has not moved since), or when the winner has zero candidates: a list
// never opens empty. Candidates beyond MaxCandidates are dropped.
//
// A no-match, or a match whose {source, start} differs from the
// dismissed span, clears the dismissal — so a new trigger elsewhere in
// the line, or on a following keystroke, opens normally.
//
// cursor is clamped to [0, len(line)] so a caller need not guard a
// stale cursor position itself.
func (e *Engine) Complete(line string, cursor int, sources []Source) (Result, bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	for _, s := range sources {
		start, term, ok := s.Match(line, cursor)
		if !ok {
			continue
		}
		key := dismissKey{s.Name(), start}
		if e.dismissedSet && e.dismissed == key {
			return Result{}, false
		}
		e.dismissedSet = false
		candidates := s.Candidates(term)
		if len(candidates) == 0 {
			return Result{}, false
		}
		if len(candidates) > MaxCandidates {
			candidates = candidates[:MaxCandidates]
		}
		return Result{Source: s.Name(), Start: start, End: cursor,
			Candidates: candidates}, true
	}
	e.dismissedSet = false
	return Result{}, false
}

// Dismiss keeps r's span closed until its trigger fires with a
// different start (further typing inside the same span, which only
// moves the end, stays quiet). Typically called from esc.
func (e *Engine) Dismiss(r Result) {
	e.dismissed = dismissKey{r.Source, r.Start}
	e.dismissedSet = true
}
