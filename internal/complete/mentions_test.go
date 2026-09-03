package complete

import "testing"

// TestMentionsMatch ports the trigger edge cases the plan calls out:
// \B keeps an e-mail-shaped run from opening a list, a bare "@" opens
// with an empty term, and the reported start is a byte offset even
// when the line carries multi-byte runes before the "@".
func TestMentionsMatch(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		cursor    int
		wantStart int
		wantTerm  string
		wantOK    bool
	}{
		{"prefix mid-line", "hi @ali", 7, 3, "ali", true},
		{"prefix at line start", "@alice", 6, 0, "alice", true},
		{"bare at", "@", 1, 0, "", true},
		{"no boundary before at", "foo@bar", 7, 0, "", false},
		{"group mention text matches", "@!", 2, 0, "!", true},
		{"multibyte before at is a byte offset", "héllo @a", len("héllo @a"), len("héllo "), "a", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := Mentions{}
			start, term, ok := m.Match(tc.line, tc.cursor)
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

// mentionUsers builds a Users func for tests from a fixed roster, with
// a counter so TestMentionsUsersCalledEveryQuery can assert freshness.
func mentionUsers(users []User) (func() []User, *int) {
	calls := 0
	return func() []User {
		calls++
		out := make([]User, len(users))
		copy(out, users)
		return out
	}, &calls
}

func selfName(name string) func() string {
	return func() string { return name }
}

// TestMentionsSelfExcluded checks the case-insensitive self exclusion:
// a roster row "bob" is dropped when Self reports "Bob".
func TestMentionsSelfExcluded(t *testing.T) {
	users, _ := mentionUsers([]User{{Name: "bob"}, {Name: "alice"}})
	m := Mentions{Users: users, Self: selfName("Bob")}
	got := m.Candidates("")
	if len(got) != 1 || got[0].Label != "alice" {
		t.Fatalf("Candidates(\"\") = %+v, want only alice", got)
	}
}

// TestMentionsAwayDimmed checks that an away user is kept (not
// dropped, unlike self), marked Dim, and carries the "away" detail the
// UI draws faint next to the name.
func TestMentionsAwayDimmed(t *testing.T) {
	users, _ := mentionUsers([]User{{Name: "alice", Away: true}, {Name: "bob"}})
	m := Mentions{Users: users, Self: selfName("")}
	got := m.Candidates("al")
	if len(got) != 1 {
		t.Fatalf("Candidates(\"al\") = %+v, want 1", got)
	}
	if !got[0].Dim || got[0].Detail != "away" {
		t.Errorf("away candidate = %+v, want Dim with Detail \"away\"", got[0])
	}
	if got[0].Insert != "@alice " {
		t.Errorf("Insert = %q, want \"@alice \"", got[0].Insert)
	}
}

// TestMentionsBareAtSortedLikeWho: an empty term returns everyone
// (minus self), case-insensitive alphabetical — the same order /who
// prints its roster in.
func TestMentionsBareAtSortedLikeWho(t *testing.T) {
	users, _ := mentionUsers([]User{
		{Name: "carol"}, {Name: "Bob"}, {Name: "alice"}, {Name: "me"},
	})
	m := Mentions{Users: users, Self: selfName("me")}
	got := m.Candidates("")
	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	want := []string{"alice", "Bob", "carol"}
	if len(labels) != len(want) {
		t.Fatalf("Candidates(\"\") labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("Candidates(\"\") labels = %v, want %v", labels, want)
			break
		}
	}
}

// TestMentionsRanking exercises the three tiers together: a prefix
// match sorts before a substring match, which sorts before a
// subsequence match, alphabetical within each tier. "Aldan" contains
// "an" contiguously (ald-AN), so it lands in the substring tier rather
// than the subsequence one; "Aiden" (a...d...e...n, no contiguous "an")
// is the one genuine subsequence-only match, so it stands in for the
// plan's worked example instead.
func TestMentionsRanking(t *testing.T) {
	users, _ := mentionUsers([]User{
		{Name: "Sam"}, {Name: "Anna"}, {Name: "brian"}, {Name: "hank"}, {Name: "Aiden"},
	})
	m := Mentions{Users: users, Self: selfName("")}
	got := m.Candidates("an")
	var labels []string
	for _, c := range got {
		labels = append(labels, c.Label)
	}
	want := []string{"Anna", "brian", "hank", "Aiden"}
	if len(labels) != len(want) {
		t.Fatalf("Candidates(\"an\") labels = %v, want %v", labels, want)
	}
	for i := range want {
		if labels[i] != want[i] {
			t.Errorf("Candidates(\"an\") labels = %v, want %v", labels, want)
			break
		}
	}
}

// TestMentionsGroupMentionPassesThrough: "@!" matches the trigger (the
// plan requires this so the text can still be typed), but no username
// can contain "!", so Candidates finds nothing and the engine leaves
// the list closed.
func TestMentionsGroupMentionPassesThrough(t *testing.T) {
	users, _ := mentionUsers([]User{{Name: "alice"}, {Name: "bob"}})
	m := Mentions{Users: users, Self: selfName("")}
	_, term, ok := m.Match("@!", 2)
	if !ok || term != "!" {
		t.Fatalf("Match(\"@!\") = (%q, %v), want term \"!\" ok true", term, ok)
	}
	if got := m.Candidates(term); len(got) != 0 {
		t.Errorf("Candidates(\"!\") = %+v, want none", got)
	}
}

// TestMentionsUsersCalledEveryQuery guards the query-time read: Users
// must be invoked on every Candidates call, not memoized, since that is
// what lets a roster change between keystrokes show up without a hook.
func TestMentionsUsersCalledEveryQuery(t *testing.T) {
	users, calls := mentionUsers([]User{{Name: "alice"}})
	m := Mentions{Users: users, Self: selfName("")}
	m.Candidates("")
	m.Candidates("a")
	if *calls != 2 {
		t.Errorf("Users called %d times, want 2 (once per Candidates call)", *calls)
	}
}
