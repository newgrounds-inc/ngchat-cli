package complete

import (
	"fmt"
	"reflect"
	"regexp"
	"testing"
)

// stubTrigger ports the mention regex from upstream's
// regexes.test.ts (src/client/app/chat/autocomplete/strategies), which
// is what the trigger-detection and span tests below exercise.
var stubTrigger = regexp.MustCompile(`\B@([a-zA-Z0-9-!*]*)$`)

// stubSource is a minimal @-triggered Source for engine tests: no
// roster, no ranking rules beyond the shared Rank helper.
type stubSource struct {
	name  string
	names []string
}

func (s stubSource) Name() string { return s.name }

func (s stubSource) Match(line string, cursor int) (int, string, bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	head := line[:cursor]
	loc := stubTrigger.FindStringSubmatchIndex(head)
	if loc == nil {
		return 0, "", false
	}
	return loc[0], head[loc[2]:loc[3]], true
}

func (s stubSource) Candidates(term string) []Candidate {
	names := Rank(term, s.names, func(n string) string { return n })
	out := make([]Candidate, len(names))
	for i, n := range names {
		out[i] = Candidate{Insert: "@" + n + " ", Label: "@" + n}
	}
	return out
}

func TestStubTriggerMatchesUpstreamCases(t *testing.T) {
	// Ported from regexes.test.ts's "mention strategy regex" cases,
	// plus the \B guard's own test ("does not match mid-sentence" is
	// the slash strategy's version of the same idea for @).
	tests := []struct {
		line     string
		wantOK   bool
		wantTerm string
	}{
		{"hi @ali", true, "ali"},
		{"@alice", true, "alice"},
		{"@", true, ""},
		{"foo@bar", false, ""},
	}
	s := stubSource{name: "stub"}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			_, term, ok := s.Match(tc.line, len(tc.line))
			if ok != tc.wantOK {
				t.Fatalf("Match(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if ok && term != tc.wantTerm {
				t.Errorf("Match(%q) term = %q, want %q", tc.line, term, tc.wantTerm)
			}
		})
	}
}

func TestStubSpanBounds(t *testing.T) {
	s := stubSource{name: "stub"}

	line := "hi @ali"
	start, term, ok := s.Match(line, len(line))
	if !ok || start != 3 || term != "ali" {
		t.Fatalf("end-of-line: start=%d term=%q ok=%v, want 3 %q true", start, term, ok, "ali")
	}

	// "@al|ice and more" — cursor lands after "al", mid-word.
	line2 := "@alice and more"
	cursor := len("@al")
	start2, term2, ok2 := s.Match(line2, cursor)
	if !ok2 || start2 != 0 || term2 != "al" {
		t.Fatalf("mid-line: start=%d term=%q ok=%v, want 0 %q true", start2, term2, ok2, "al")
	}
}

// TestRank exercises the three-tier order (prefix, substring,
// subsequence), alphabetical within a tier, case-insensitivity, and
// that a non-match is dropped rather than sorted to the end.
func TestRank(t *testing.T) {
	items := []string{"Sam", "mark", "Amanda", "Emma", "moa"}

	got := Rank("ma", items, func(s string) string { return s })
	want := []string{"mark", "Amanda", "Emma", "moa"} // Sam matches nothing
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Rank(%q) = %v, want %v", "ma", got, want)
	}

	all := Rank("", items, func(s string) string { return s })
	wantAll := []string{"Amanda", "Emma", "mark", "moa", "Sam"}
	if !reflect.DeepEqual(all, wantAll) {
		t.Errorf(`Rank("") = %v, want %v`, all, wantAll)
	}
}

// TestRankStableOnTies confirms equal keys keep their input order,
// which is what lets a source's own ordering survive a tie.
func TestRankStableOnTies(t *testing.T) {
	items := []string{"b:2", "b:1", "a:1"}
	key := func(s string) string { return s[:1] }
	got := Rank("", items, key)
	want := []string{"a:1", "b:2", "b:1"} // "b" items keep b:2 before b:1
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Rank stability = %v, want %v", got, want)
	}
}

// TestRankTieBreaksOnExactKey confirms a tie on the lowercased key
// (e.g. a roster carrying both "Bob" and "bob", which a map range can
// hand back in either order) resolves on the exact key instead of
// falling through to input order, so the result is the same regardless
// of which one the caller happened to see first.
func TestRankTieBreaksOnExactKey(t *testing.T) {
	key := func(s string) string { return s }
	want := []string{"Bob", "bob"} // "B" (0x42) sorts before "b" (0x62)

	got := Rank("", []string{"bob", "Bob"}, key)
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`Rank("", [bob, Bob]) = %v, want %v`, got, want)
	}

	got = Rank("", []string{"Bob", "bob"}, key)
	if !reflect.DeepEqual(got, want) {
		t.Errorf(`Rank("", [Bob, bob]) = %v, want %v`, got, want)
	}
}

// capSource always matches at the start of the line and offers more
// than MaxCandidates candidates, for the truncation test.
type capSource struct{ n int }

func (capSource) Name() string { return "cap" }
func (capSource) Match(line string, cursor int) (int, string, bool) {
	return 0, line[:cursor], true
}
func (s capSource) Candidates(string) []Candidate {
	out := make([]Candidate, s.n)
	for i := range out {
		out[i] = Candidate{Insert: fmt.Sprintf("c%d", i)}
	}
	return out
}

func TestCompleteCapsCandidates(t *testing.T) {
	var e Engine
	res, ok := e.Complete("x", 1, []Source{capSource{n: 12}})
	if !ok {
		t.Fatal("Complete() ok = false, want true")
	}
	if len(res.Candidates) != MaxCandidates {
		t.Errorf("candidates = %d, want %d", len(res.Candidates), MaxCandidates)
	}
}

// zeroSource matches but never has anything to offer: the list must
// never open empty.
type zeroSource struct{}

func (zeroSource) Name() string                          { return "zero" }
func (zeroSource) Match(string, int) (int, string, bool) { return 0, "", true }
func (zeroSource) Candidates(string) []Candidate         { return nil }

func TestCompleteNeverOpensEmpty(t *testing.T) {
	var e Engine
	_, ok := e.Complete("anything", len("anything"), []Source{zeroSource{}})
	if ok {
		t.Error("Complete() with zero candidates should report ok=false")
	}
}

func TestCompleteFirstSourceWins(t *testing.T) {
	first := stubSource{name: "first", names: []string{"alice"}}
	second := stubSource{name: "second", names: []string{"alice"}}
	var e Engine
	res, ok := e.Complete("@al", 3, []Source{first, second})
	if !ok {
		t.Fatal("Complete() ok = false, want true")
	}
	if res.Source != "first" {
		t.Errorf("Source = %q, want %q (first source to match wins)", res.Source, "first")
	}
}

func TestCompleteNoMatch(t *testing.T) {
	var e Engine
	_, ok := e.Complete("hello world", 11, []Source{stubSource{name: "stub"}})
	if ok {
		t.Error("Complete() should report ok=false for an untriggered line")
	}
}

var stubNames = []string{"alice", "alicia", "bob", "carol", "dave", "erin", "frank"}

func TestDismissStaysQuietUntilSpanMoves(t *testing.T) {
	var e Engine
	s := stubSource{name: "stub", names: stubNames}

	res, ok := e.Complete("hi @a", 5, []Source{s})
	if !ok {
		t.Fatal("initial match failed to open")
	}
	e.Dismiss(res)

	// More typing in the same span ("@a" -> "@al") stays quiet.
	if _, ok := e.Complete("hi @al", 6, []Source{s}); ok {
		t.Error("dismissed span reopened after more typing in the same span")
	}

	// A second "@" later in the line is a different start: reopens.
	if _, ok := e.Complete("hi @al @b", 9, []Source{s}); !ok {
		t.Error("a new trigger with a different start should reopen")
	}

	// A no-match followed by a new trigger also reopens.
	e = Engine{}
	res, ok = e.Complete("@a", 2, []Source{s})
	if !ok {
		t.Fatal("initial match failed to open")
	}
	e.Dismiss(res)
	if _, ok := e.Complete("@a ", 3, []Source{s}); ok {
		t.Fatal("trailing space should not match the stub trigger")
	}
	if _, ok := e.Complete("@a b", 4, []Source{s}); ok {
		t.Fatal("no trigger active here")
	}
	if _, ok := e.Complete("@a @b", 5, []Source{s}); !ok {
		t.Error("a fresh trigger after a no-match should reopen")
	}
}
