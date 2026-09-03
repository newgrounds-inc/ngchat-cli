package complete

import "testing"

// TestEmotesMatch ports the trigger edge cases from upstream
// regexes.test.ts's "dankmeme strategy regex" block, plus two cases
// specific to this port: "@ngfoo" (no whitespace before the token, so
// the mention source owns it, never this one) and a multi-word line to
// check the byte offset lands on the token, not the whole line.
func TestEmotesMatch(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantStart int
		wantTerm  string
		wantOK    bool
	}{
		{"ng prefix, whole match is the term", "ngsmile", 0, "ngsmile", true},
		{"ng prefix mid-line", "hi ngparty", 3, "ngparty", true},
		{"tf prefix", "tfsad", 0, "tfsad", true},
		{"wildcard", "ng*smile", 0, "ng*smile", true},
		{"shortest real ng name", "ngaK", 0, "ngaK", true},
		{"shortest real tf name", "tfPls", 0, "tfPls", true},
		{"bare ng", "ng", 0, "", false},
		{"nga alone", "nga", 0, "", false},
		{"ngl alone", "ngl", 0, "", false},
		{"bare tf", "tf", 0, "", false},
		{"tfw alone", "tfw", 0, "", false},
		{"hey tf", "hey tf", 0, "", false},
		{"mid-word never matches", "wtfBedn", 0, "", false},
		{"no whitespace before token", "@ngfoo", 0, "", false},
		{"multibyte prefix", "héllo ngaho", 7, "ngaho", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := Emotes{}
			start, term, ok := e.Match(tc.line, len(tc.line))
			if ok != tc.wantOK {
				t.Fatalf("Match(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if start != tc.wantStart || term != tc.wantTerm {
				t.Errorf("Match(%q) = (%d, %q), want (%d, %q)",
					tc.line, start, term, tc.wantStart, tc.wantTerm)
			}
		})
	}
}

// TestEmotesMatchNgleTriggersRegardlessOfCandidates: Match and
// Candidates are independent questions. "ngle" triggers the regex (ng
// + one more character, "l", plus one more, "e") regardless of what
// Candidates does with it — no shortcode has "ngle" as a prefix, but
// several match it at the subsequence tier, so this checks only
// Match's answer, not the (non-empty) candidate list.
func TestEmotesMatchNgleTriggersRegardlessOfCandidates(t *testing.T) {
	e := Emotes{}
	_, term, ok := e.Match("ngle", len("ngle"))
	if !ok || term != "ngle" {
		t.Fatalf("Match(%q) = (%q, %v), want (\"ngle\", true)", "ngle", term, ok)
	}
}

// TestEmotesCandidatesRanksPrefixFirst: the prefix tier puts
// "ngaHoldup" first for "ngaho" (alphabetically first among the
// "ngaho*" prefix matches, case-insensitively). Candidates truncates
// to MaxCandidates, so the no-match check ("tfPls" must be dropped)
// runs against the full Rank output, where a subsequence false
// positive could not hide past the cap.
func TestEmotesCandidatesRanksPrefixFirst(t *testing.T) {
	e := Emotes{}
	got := e.Candidates("ngaho")
	if len(got) == 0 {
		t.Fatal("Candidates(\"ngaho\") = empty, want at least one match")
	}
	if got[0].Label != "ngaHoldup" {
		t.Errorf("Candidates(\"ngaho\")[0] = %q, want \"ngaHoldup\"", got[0].Label)
	}
	if got[0].Insert != "ngaHoldup " {
		t.Errorf("Candidates(\"ngaho\")[0].Insert = %q, want \"ngaHoldup \"", got[0].Insert)
	}
	for _, code := range Rank("ngaho", emoteCodes, identity) {
		if code == "tfPls" {
			t.Error("Rank(\"ngaho\") includes \"tfPls\", want it dropped")
		}
	}
}

// TestEmotesCandidatesWildcardStripped: "*" in the term is removed
// before ranking, so "ng*holdup" (no real shortcode contains a literal
// "*") still finds "ngaHoldup" via the subsequence tier over the
// stripped term "ngholdup".
func TestEmotesCandidatesWildcardStripped(t *testing.T) {
	e := Emotes{}
	got := e.Candidates("ng*holdup")
	found := false
	for _, c := range got {
		if c.Label == "ngaHoldup" {
			found = true
		}
	}
	if !found {
		t.Errorf("Candidates(\"ng*holdup\") = %v, want ngaHoldup present", got)
	}
}

// TestEmotesCandidatesEmptyForNoMatch: a term matching nothing in the
// embedded list returns no candidates, which is what Engine.Complete
// relies on to keep the list from opening at all.
func TestEmotesCandidatesEmptyForNoMatch(t *testing.T) {
	e := Emotes{}
	if got := e.Candidates("ngzzzzz"); len(got) != 0 {
		t.Errorf("Candidates(\"ngzzzzz\") = %v, want none", got)
	}
}

// TestEmoteCodesEmbedded checks the generated data's shape rather than
// its exact contents, which would make the test brittle against a
// routine regeneration: a floor on the count, the two names the spec
// calls out by name, no duplicates, and a case-insensitive sort with
// exact-string tiebreaking (mirroring the generator's own rule).
func TestEmoteCodesEmbedded(t *testing.T) {
	if len(emoteCodes) < 1000 {
		t.Fatalf("len(emoteCodes) = %d, want >= 1000", len(emoteCodes))
	}
	var hasRandom, hasTfPls bool
	seen := make(map[string]bool, len(emoteCodes))
	for i, code := range emoteCodes {
		if code == "ngrandom" {
			hasRandom = true
		}
		if code == "tfPls" {
			hasTfPls = true
		}
		if seen[code] {
			t.Errorf("emoteCodes has a duplicate: %q", code)
		}
		seen[code] = true
		if i > 0 {
			prev, cur := lowerFold(emoteCodes[i-1]), lowerFold(code)
			if prev > cur || (prev == cur && emoteCodes[i-1] > code) {
				t.Errorf("emoteCodes not sorted at %d: %q before %q",
					i, emoteCodes[i-1], code)
			}
		}
	}
	if !hasRandom {
		t.Error("emoteCodes missing \"ngrandom\"")
	}
	if !hasTfPls {
		t.Error("emoteCodes missing \"tfPls\"")
	}
}

// lowerFold is a tiny local wrapper so TestEmoteCodesEmbedded reads as
// a direct mirror of the generator's own sort rule.
func lowerFold(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// TestEmotesDismissStaysQuietUntilSpanMoves runs the sticky-dismiss
// contract through the real Engine, the same shape as
// complete_test.go's phase-0 coverage, but for the emote source: esc
// on an ordinary "ng…" word (one that happens to open a list) keeps
// quiet through further typing in the same word, and a new word (a new
// span start) opens again.
func TestEmotesDismissStaysQuietUntilSpanMoves(t *testing.T) {
	var e Engine
	sources := []Source{Emotes{}}

	res, ok := e.Complete("ngaho", 5, sources)
	if !ok {
		t.Fatal("Complete(\"ngaho\") = not ok, want a match")
	}
	e.Dismiss(res)

	if _, ok := e.Complete("ngahol", 6, sources); ok {
		t.Error("Complete(\"ngahol\") after Dismiss = ok, want it to stay closed")
	}

	if _, ok := e.Complete(" ngaho", 6, sources); !ok {
		t.Error("Complete(\" ngaho\") (a new span start) = not ok, want it to open")
	}
}
