package complete

import "testing"

// TestCommandsMatch ports the whole-line trigger cases the spec calls
// out: the command list only opens while "/" is the first character of
// the line and nothing but a bare prefix follows it, case-insensitive.
func TestCommandsMatch(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantStart int
		wantTerm  string
		wantOK    bool
	}{
		{"bare slash", "/", 0, "", true},
		{"name prefix", "/ban", 0, "ban", true},
		{"path-shaped prefix", "/block/a", 0, "block/a", true},
		{"wildcard prefix", "/k*", 0, "k*", true},
		{"mid-line never triggers", "hello /ban", 0, "", false},
		{"argument text closes the trigger", "/x y", 0, "", false},
		{"case-insensitive, term keeps case", "/BAN", 0, "BAN", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := Commands{}
			start, term, ok := c.Match(tc.line, len(tc.line))
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

func viewerFunc(a Access) func() Access { return func() Access { return a } }

// commandNames extracts Label from a Candidates result in order, for
// tests that only care about which commands showed up.
func commandNames(cands []Candidate) []string {
	names := make([]string, len(cands))
	for i, c := range cands {
		names[i] = c.Label
	}
	return names
}

// hasName reports whether any candidate inserts name's canonical form,
// regardless of what term or alias found it.
func hasName(cands []Candidate, name string) bool {
	for _, c := range cands {
		if c.Insert == name+" " {
			return true
		}
	}
	return false
}

// TestCommandsAccessFilter checks that each rank sees exactly the
// commands at or below it: AccessAll never sees a mod- or admin-gated
// command, AccessMod sees mod commands but not admin ones, and
// AccessAdmin sees everything the table has.
func TestCommandsAccessFilter(t *testing.T) {
	all := Commands{Viewer: viewerFunc(AccessAll)}.Candidates("")
	if hasName(all, "/kick") || hasName(all, "/play") {
		t.Errorf("AccessAll candidates = %v, want no /kick or /play", commandNames(all))
	}
	var wantAllCount int
	for _, cmd := range CommandTable {
		if cmd.Access == AccessAll {
			wantAllCount++
		}
	}
	if len(all) != wantAllCount {
		t.Errorf("AccessAll candidate count = %d, want %d (AccessAll rows in the table)",
			len(all), wantAllCount)
	}

	mod := Commands{Viewer: viewerFunc(AccessMod)}.Candidates("")
	if !hasName(mod, "/kick") {
		t.Errorf("AccessMod candidates = %v, want /kick", commandNames(mod))
	}
	if hasName(mod, "/play") || hasName(mod, "/disconnect") {
		t.Errorf("AccessMod candidates = %v, want no /play or /disconnect", commandNames(mod))
	}

	admin := Commands{Viewer: viewerFunc(AccessAdmin)}.Candidates("")
	if len(admin) != len(CommandTable) {
		t.Errorf("AccessAdmin candidate count = %d, want %d (every row)",
			len(admin), len(CommandTable))
	}
}

// TestCommandsAliasInsertsCanonicalName checks that a term matching
// only an alias still offers the command under its canonical name and
// inserts that name, never the alias.
func TestCommandsAliasInsertsCanonicalName(t *testing.T) {
	mod := Commands{Viewer: viewerFunc(AccessMod)}
	got := mod.Candidates("k")
	if len(got) != 1 || got[0].Label != "/kick (k)" || got[0].Insert != "/kick " {
		t.Fatalf("Candidates(\"k\") = %+v, want one /kick candidate inserting \"/kick \"", got)
	}

	got = mod.Candidates("airhorn")
	if len(got) != 1 || got[0].Label != "/ah (airhorn)" || got[0].Insert != "/ah " {
		t.Fatalf("Candidates(\"airhorn\") = %+v, want one /ah candidate inserting \"/ah \"", got)
	}
}

// TestCommandsWildcard exercises upstream's "*" -> ".*" wildcard rule
// against a couple of worked examples from the spec.
func TestCommandsWildcard(t *testing.T) {
	mod := Commands{Viewer: viewerFunc(AccessMod)}
	got := commandNames(mod.Candidates("b*n"))
	if want := []string{"/ban (b)"}; !equalStrings(got, want) {
		t.Errorf("Candidates(\"b*n\") = %v, want %v", got, want)
	}

	all := Commands{Viewer: viewerFunc(AccessAll)}
	got = commandNames(all.Candidates("*list"))
	want := []string{"/block/list", "/ignore/list"}
	if !equalStrings(got, want) {
		t.Errorf("Candidates(\"*list\") = %v, want %v", got, want)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestCommandsBareSlashListsEverythingAlphabetical checks the "/"
// case: every command the viewer may use, alphabetical, with the cap
// left to Engine.Complete rather than Commands itself.
func TestCommandsBareSlashListsEverythingAlphabetical(t *testing.T) {
	admin := Commands{Viewer: viewerFunc(AccessAdmin)}
	got := commandNames(admin.Candidates(""))
	if len(got) != len(CommandTable) {
		t.Fatalf("Candidates(\"\") count = %d, want %d", len(got), len(CommandTable))
	}
	wantFirst := []string{"/ah (airhorn)", "/alts", "/away (a)"}
	if !equalStrings(got[:3], wantFirst) {
		t.Errorf("Candidates(\"\") first three = %v, want %v", got[:3], wantFirst)
	}

	var e Engine
	res, ok := e.Complete("/", 1, []Source{admin})
	if !ok {
		t.Fatal("Engine.Complete(\"/\") did not open")
	}
	if len(res.Candidates) != MaxCandidates {
		t.Errorf("Engine.Complete(\"/\") candidates = %d, want the %d cap",
			len(res.Candidates), MaxCandidates)
	}

	// CommandTable declares /who after /yippee (appended, not
	// interleaved alphabetically), so this only holds if Candidates
	// actually sorts its filtered result rather than relying on the
	// table's own order. AccessAll excludes /unban (mod-only), so
	// /table/r and /yippee are /who's immediate alphabetical
	// neighbors once the mod-only rows are filtered out.
	all := commandNames(Commands{Viewer: viewerFunc(AccessAll)}.Candidates(""))
	whoIdx := -1
	for i, n := range all {
		if n == "/who" {
			whoIdx = i
			break
		}
	}
	if whoIdx <= 0 || whoIdx >= len(all)-1 ||
		all[whoIdx-1] != "/table/r" || all[whoIdx+1] != "/yippee" {
		t.Fatalf("AccessAll order around /who = %v, want ...,/table/r,/who,/yippee,...",
			all)
	}
}

// TestCommandsLabel checks the three label shapes: one alias, more
// than one, and none.
func TestCommandsLabel(t *testing.T) {
	admin := Commands{Viewer: viewerFunc(AccessAdmin)}

	kick := admin.Candidates("kick")
	if len(kick) != 1 || kick[0].Label != "/kick (k)" {
		t.Fatalf("Candidates(\"kick\") = %+v, want Label \"/kick (k)\"", kick)
	}

	ah := admin.Candidates("ah")
	if len(ah) != 1 || ah[0].Label != "/ah (airhorn)" {
		t.Fatalf("Candidates(\"ah\") = %+v, want Label \"/ah (airhorn)\"", ah)
	}

	dm := admin.Candidates("dm")
	if len(dm) != 1 || dm[0].Label != "/dm" {
		t.Fatalf("Candidates(\"dm\") = %+v, want Label \"/dm\" with no parens", dm)
	}
	if dm[0].Detail != "sends a direct message to a user" {
		t.Errorf("Candidates(\"dm\") Detail = %q, want the table description", dm[0].Detail)
	}
}

// TestAccessFor checks the three rank combinations the wire's two
// independent flags can produce.
func TestAccessFor(t *testing.T) {
	tests := []struct {
		isAdmin, isChatMod bool
		want               Access
	}{
		{true, true, AccessAdmin},
		{false, true, AccessMod},
		{false, false, AccessAll},
		{true, false, AccessAdmin},
	}
	for _, tc := range tests {
		if got := AccessFor(tc.isAdmin, tc.isChatMod); got != tc.want {
			t.Errorf("AccessFor(%v, %v) = %v, want %v",
				tc.isAdmin, tc.isChatMod, got, tc.want)
		}
	}

	// Access's doc comment promises an ascending rank (Candidates
	// relies on cmd.Access <= Viewer()), so the three constants must
	// order this way regardless of their underlying values.
	if !(AccessAll < AccessMod && AccessMod < AccessAdmin) {
		t.Errorf("Access rank order = %d < %d < %d is false, want ascending",
			AccessAll, AccessMod, AccessAdmin)
	}
}

// TestCommandTableSanity guards the hand-ported table itself: every
// name is well-formed, nothing collides, and the CLI-local /who made
// it into the table.
func TestCommandTableSanity(t *testing.T) {
	// 32 upstream rows plus the CLI-local /who; bump deliberately when
	// reconciling against a new upstream commit.
	if len(CommandTable) != 33 {
		t.Fatalf("len(CommandTable) = %d, want 33", len(CommandTable))
	}

	names := map[string]bool{}
	aliases := map[string]bool{}
	sawWho := false
	for _, cmd := range CommandTable {
		if len(cmd.Name) == 0 || cmd.Name[0] != '/' {
			t.Errorf("command %q does not start with /", cmd.Name)
		}
		if names[cmd.Name] {
			t.Errorf("duplicate command name %q", cmd.Name)
		}
		names[cmd.Name] = true
		if cmd.Name == "/who" {
			sawWho = true
		}
		for _, a := range cmd.Aliases {
			if aliases[a] {
				t.Errorf("duplicate alias %q (command %q)", a, cmd.Name)
			}
			aliases[a] = true
		}
	}
	if !sawWho {
		t.Error("CommandTable is missing /who")
	}
}
