package complete

import (
	"regexp"
	"strings"
)

// mentionTrigger is upstream's mention regex, defined in
// src/client/config.ts and used by
// strategies/mention_strategy.tsx; regexes.test.ts is where the edge
// cases this port is checked against come from, not the regex's home.
// Ported unchanged, less upstream's /i flag: it is a no-op here since
// the character class below already spans both cases. \B keeps a
// non-word boundary in front of @ so "foo@bar" (an e-mail-shaped run of
// text) never opens a list, while "hi @al" and a line-start "@al" do.
// The character class matches every byte a site username can contain;
// "!" and "*" are included only because a person can type them by hand
// ("@!" for a group mention) and the trigger must still fire so
// Candidates gets a chance to find (and correctly find none of) a
// match.
var mentionTrigger = regexp.MustCompile(`\B@([a-zA-Z0-9-!*]*)$`)

// User is the slice of a roster row completion needs: enough to rank
// and to draw a row, nothing that would couple this package to the
// wire format.
type User struct {
	Name string
	Away bool
}

// Mentions completes @-mentions from the live roster. Unlike a source
// backed by static data, both fields are called fresh on every query
// rather than captured once, so a join, a leave or an away change
// between keystrokes is reflected without any change notification
// flowing into this package. Both fields are required: Candidates
// calls them unguarded, so a zero-value Mentions panics on first use
// rather than silently completing nothing.
type Mentions struct {
	// Users returns the current roster. Called on every query, so the
	// list is never stale; the UI hands in a closure over its map.
	Users func() []User
	// Self is excluded from the candidates; compared case-insensitively.
	Self func() string
}

// Name identifies the mention source for Engine's sticky dismiss.
func (Mentions) Name() string { return "mention" }

// Match reports the span of an open @-mention, per mentionTrigger. term
// is the partial name typed so far, empty right after a bare "@".
func (m Mentions) Match(line string, cursor int) (start int, term string, ok bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	head := line[:cursor]
	loc := mentionTrigger.FindStringSubmatchIndex(head)
	if loc == nil {
		return 0, "", false
	}
	return loc[0], head[loc[2]:loc[3]], true
}

// Candidates ranks the current roster against term: an empty term
// yields everyone alphabetical, like /who; a non-empty one goes through
// the shared three-tier Rank. Self is dropped first so it never
// occupies a ranking slot. A name containing "!" or "*" cannot exist on
// the site, so a hand-typed "@!" or "@*" simply ranks against nothing
// and the list stays closed (Engine never opens an empty result).
func (m Mentions) Candidates(term string) []Candidate {
	self := m.Self()
	var users []User
	for _, u := range m.Users() {
		if !strings.EqualFold(u.Name, self) {
			users = append(users, u)
		}
	}
	ranked := Rank(term, users, func(u User) string { return u.Name })
	out := make([]Candidate, len(ranked))
	for i, u := range ranked {
		detail := ""
		if u.Away {
			detail = "away"
		}
		out[i] = Candidate{
			Insert: "@" + u.Name + " ",
			Label:  u.Name,
			Detail: detail,
			Dim:    u.Away,
		}
	}
	return out
}
