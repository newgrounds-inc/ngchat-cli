package transcript

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/newgrounds-inc/ngchat-cli/internal/theme"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;;[^\x1b]*\x1b\\`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

var when = time.Date(2026, 9, 2, 13, 5, 0, 0, time.Local)

func chat(name, html string) Row {
	return Row{Kind: Chat, Username: name, HTML: html, At: when}
}

func event(text string) Row {
	return Row{Kind: Event, Text: text, At: when}
}

// drawn is the plain text of one row as the transcript would show it.
func drawn(tr Transcript, r Row) string { return plain(tr.draw(r)) }

func TestDrawFormatsSpeaker(t *testing.T) {
	tr := New(theme.Default())
	tests := []struct {
		name string
		row  Row
		want string
	}{
		{"chat", chat("bob", "hi"), "<bob> hi"},
		{"dm", Row{Kind: DM, Username: "bob", HTML: "psst"}, "[DM] <bob> psst"},
		// The server's HTML already names the actor, so the row must
		// not repeat it.
		{"me", Row{Kind: Me, Username: "bob", HTML: "bob waves"}, "* bob waves"},
		{"slap", Row{Kind: Slap, Username: "bob", HTML: "bob slaps ann"}, "* bob slaps ann"},
		{"server", Row{Kind: Server, HTML: "<b>maintenance</b> soon"}, "maintenance soon"},
		{"event", event("bob joined"), "bob joined"},
		{"error", Row{Kind: Error, Text: "send failed"}, "send failed"},
		{"roster", Row{Kind: Roster, Text: "2 users: ann, bob"}, "2 users: ann, bob"},
		{"gap", Row{Kind: Gap}, "— reconnected, older messages missing —"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := drawn(tr, tc.row); got != tc.want {
				t.Errorf("row = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDrawConvertsHTML: rows go through the render package rather than
// printing raw markup.
func TestDrawConvertsHTML(t *testing.T) {
	tr := New(theme.Default())
	got := drawn(tr, chat("bob", `say <strong>hi</strong> to <a href="https://x.test">x</a>`))
	if strings.Contains(got, "<strong>") {
		t.Errorf("raw HTML reached the transcript: %q", got)
	}
	if !strings.Contains(got, "say hi to x") {
		t.Errorf("row = %q", got)
	}
}

// TestSpoilerToggle: the raw HTML stays on the row so a reveal draws
// from source instead of losing the hidden text.
func TestSpoilerToggle(t *testing.T) {
	tr := New(theme.Default())
	row := Row{Kind: Chat, Username: "bob", HTML: "the butler did it", Spoiler: true, At: when}

	hidden := drawn(tr, row)
	if strings.Contains(hidden, "butler") {
		t.Errorf("spoiler text leaked while hidden: %q", hidden)
	}
	if !strings.Contains(hidden, "ctrl+s") {
		t.Errorf("hidden spoiler should say how to reveal it: %q", hidden)
	}

	tr.SetOptions(Options{RevealSpoilers: true})
	if revealed := drawn(tr, row); !strings.Contains(revealed, "the butler did it") {
		t.Errorf("revealed spoiler = %q", revealed)
	}
}

func TestTimestampOption(t *testing.T) {
	tr := New(theme.Default())
	if got := drawn(tr, chat("bob", "hi")); got != "<bob> hi" {
		t.Errorf("row without times = %q", got)
	}
	tr.SetOptions(Options{ShowTimes: true})
	if got := drawn(tr, chat("bob", "hi")); got != "13:05 <bob> hi" {
		t.Errorf("row with times = %q", got)
	}
}

// TestNetworkTextCannotDriveTheTerminal: the username and the HTML
// arrive raw from the wire, so both are stripped of controls on the
// way to the screen. HTML keeps its line breaks (a message may span
// lines), so only the username is checked for a forged line.
func TestNetworkTextCannotDriveTheTerminal(t *testing.T) {
	const evil = "bob\x1b[2J\x1b]52;c;xxx\x07\nmallory"
	tr := New(theme.Default())
	for _, kind := range []Kind{Chat, DM, Me, Slap, Server} {
		got := tr.draw(Row{Kind: kind, Username: evil, HTML: "hi", At: when})
		if p := plain(got); strings.ContainsAny(p, "\x1b\x07\n") {
			t.Errorf("kind %d row %q carries a control or a forged line", kind, got)
		}
		got = tr.draw(Row{Kind: kind, Username: "bob", HTML: evil, At: when})
		if p := plain(got); strings.ContainsAny(p, "\x1b\x07") {
			t.Errorf("kind %d row %q carries a control from the HTML", kind, got)
		}
	}
}

func TestStylesFollowTheme(t *testing.T) {
	classic, _ := theme.Lookup("classic")
	tr := New(classic)
	if row := tr.draw(chat("bob", "hi")); !strings.Contains(row, "38;2;238;178;17") {
		t.Errorf("username does not carry classic username #eeb211: %q", row)
	}
}

// spoilers is a sized transcript of 40 spoilers long enough to wrap to
// several lines once revealed, so a toggle changes the height of every
// row above the reader.
func spoilers() Transcript {
	tr := New(theme.Default())
	tr.Resize(40, 12)
	for i := 0; i < 40; i++ {
		tr.Append(Row{Kind: Chat, Username: "bob", Spoiler: true, At: when,
			HTML: fmt.Sprintf("row %d %s", i, strings.Repeat("x", 100))})
	}
	return tr
}

// TestToggleKeepsRowUnderTop: revealing a spoiler while reading back
// must keep the same row at the top of the screen even though every
// row changed height.
func TestToggleKeepsRowUnderTop(t *testing.T) {
	tr := spoilers()
	tr.PageUp()
	tr.PageUp()
	if tr.Following() {
		t.Fatal("pgup should have left the reader scrolled up")
	}
	want := tr.locate().item
	linesHidden := tr.vp.TotalLineCount()

	for _, o := range []Options{
		{RevealSpoilers: true},                  // reveal: rows grow
		{RevealSpoilers: true, ShowTimes: true}, // times: rows shift
		{ShowTimes: true},                       // hide again: rows shrink
	} {
		tr.SetOptions(o)
		if tr.Following() {
			t.Fatalf("%+v jumped to the bottom", o)
		}
		if got := tr.locate().item; got != want {
			t.Errorf("after %+v the row under the top is %d, want %d", o, got, want)
		}
	}
	tr.SetOptions(Options{RevealSpoilers: true, ShowTimes: true})
	if tr.vp.TotalLineCount() == linesHidden {
		t.Fatal("setup: revealing the spoilers did not change the line count")
	}
}

// TestToggleAtBottomStaysAtBottom: a reader following the newest row
// keeps following it through a toggle, however the heights change.
func TestToggleAtBottomStaysAtBottom(t *testing.T) {
	tr := spoilers()
	tr.SetOptions(Options{RevealSpoilers: true})
	if !tr.Following() {
		t.Error("revealing spoilers while at the bottom scrolled up")
	}
}

// TestResizeKeepsRowUnderTop: a narrower terminal re-wraps every row,
// and the reader stays on theirs.
func TestResizeKeepsRowUnderTop(t *testing.T) {
	tr := spoilers()
	tr.SetOptions(Options{RevealSpoilers: true})
	tr.PageUp()
	tr.PageUp()
	want := tr.locate().item
	tr.Resize(25, 12)
	if tr.Following() {
		t.Fatal("the resize jumped to the bottom")
	}
	if got := tr.locate().item; got != want {
		t.Errorf("row under the top after the resize = %d, want %d", got, want)
	}
}

// TestAppendKeepsScrolledUpReader: a new row never yanks a reader who
// paged up; Follow is how they come back.
func TestAppendKeepsScrolledUpReader(t *testing.T) {
	tr := New(theme.Default())
	tr.Resize(80, 10)
	for i := 0; i < 30; i++ {
		tr.Append(event(fmt.Sprint(i)))
	}
	if !tr.Following() {
		t.Fatal("a fresh transcript should be following")
	}
	tr.PageUp()
	tr.Append(event("later"))
	if tr.Following() {
		t.Fatal("a new row yanked the scrolled-up reader")
	}
	if strings.Contains(plain(tr.View()), "later") {
		t.Error("the new row is on screen while scrolled up")
	}
	tr.Follow()
	if !tr.Following() || !strings.Contains(plain(tr.View()), "later") {
		t.Errorf("Follow did not show the newest row: %q", plain(tr.View()))
	}
	tr.PageUp()
	tr.PageDown()
	if !tr.Following() {
		t.Error("paging down from one screen up did not reach the bottom")
	}
}

// TestOptionsRoundTrip: a toggle reads the options back, flips one,
// and sets them; setting the same options again is a no-op.
func TestOptionsRoundTrip(t *testing.T) {
	tr := New(theme.Default())
	o := tr.Options()
	o.ShowTimes = true
	tr.SetOptions(o)
	if got := tr.Options(); got != (Options{ShowTimes: true}) {
		t.Errorf("options = %+v", got)
	}
	tr.SetOptions(o)
	if got := drawn(tr, chat("bob", "hi")); got != "13:05 <bob> hi" {
		t.Errorf("row after a repeated set = %q", got)
	}
}

// TestAppendTrimKeepsRowUnderTop: when the cap drops the oldest rows
// the anchored row moves up the slice; the reader must stay on it
// rather than slide by however many rows fell off.
func TestAppendTrimKeepsRowUnderTop(t *testing.T) {
	tr := New(theme.Default())
	for i := 0; i < MaxRows-1; i++ {
		tr.Append(event(fmt.Sprint(i)))
	}
	tr.Resize(80, 24)
	tr.PageUp()
	before := tr.Rows()[tr.locate().item].Text
	for i := 0; i < 5; i++ { // the last four each drop one row
		tr.Append(event("new"))
	}
	if after := tr.Rows()[tr.locate().item].Text; after != before {
		t.Errorf("row under the top = %q after the trim, want %q", after, before)
	}
	if tr.vp.TotalLineCount() != MaxRows {
		t.Errorf("lines after the trim = %d, want %d", tr.vp.TotalLineCount(), MaxRows)
	}
}

func TestCap(t *testing.T) {
	tr := New(theme.Default())
	for i := 0; i < MaxRows+50; i++ {
		tr.Append(event(fmt.Sprint(i)))
	}
	if n := len(tr.Rows()); n != MaxRows {
		t.Fatalf("rows = %d, want %d", n, MaxRows)
	}
	if got := tr.Rows()[0].Text; got != "50" {
		t.Errorf("oldest kept = %q, want the 51st appended", got)
	}
}

// TestAppendBeforeResize: rows that arrive before the terminal size
// is known are drawn on the first Resize, at the bottom.
func TestAppendBeforeResize(t *testing.T) {
	tr := New(theme.Default())
	tr.Append(event("early"))
	if tr.View() != "" {
		t.Errorf("view before a size = %q, want nothing", tr.View())
	}
	if !tr.Following() {
		t.Error("an unsized transcript should report following")
	}
	tr.Resize(80, 5)
	if !strings.Contains(plain(tr.View()), "early") {
		t.Errorf("the early row is missing after the first resize: %q", plain(tr.View()))
	}
	tr.Append(event("late"))
	if !strings.Contains(plain(tr.View()), "late") {
		t.Errorf("an appended row is missing: %q", plain(tr.View()))
	}
}

// TestShrinkKeepsFollowing: a terminal that gets smaller while the
// reader is at the bottom keeps them there. The anchor is read before
// the size changes; reading it after (as the UI once did) sees the
// bottom move out from under the offset and leaves the reader scrolled
// up with the "more messages below" banner.
func TestShrinkKeepsFollowing(t *testing.T) {
	for _, tc := range []struct {
		name string
		w, h int
	}{
		{"height only", 80, 18},
		{"width and height", 60, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := New(theme.Default())
			tr.Resize(80, 24)
			for i := 0; i < 60; i++ {
				tr.Append(event(fmt.Sprint(i)))
			}
			tr.Resize(tc.w, tc.h)
			if !tr.Following() {
				t.Errorf("shrinking to %dx%d left the reader scrolled up", tc.w, tc.h)
			}
			if !strings.Contains(plain(tr.View()), "59") {
				t.Errorf("newest row is off screen after the shrink: %q", plain(tr.View()))
			}
		})
	}
}

// TestHeightOnlyResizeKeepsOffset: the completion list opening and
// closing only changes the height, which must not scroll a reader who
// paged up.
func TestHeightOnlyResizeKeepsOffset(t *testing.T) {
	tr := New(theme.Default())
	tr.Resize(80, 21)
	for i := 0; i < 60; i++ {
		tr.Append(event(fmt.Sprint(i)))
	}
	tr.Resize(80, 18)
	tr.PageUp()
	before := tr.vp.YOffset()
	tr.Resize(80, 21)
	if after := tr.vp.YOffset(); after != before {
		t.Errorf("offset moved from %d to %d on a height-only resize", before, after)
	}
	if h := tr.Height(); h != 21 {
		t.Errorf("height = %d, want 21", h)
	}
}

// TestHeightOnlyResizeReclampsPastBottom: viewport.SetHeight (bubbles
// v2.2.1) does not reclamp the offset on its own, so growing back
// could leave it referencing lines past the end of the content; the
// transcript snaps to the bottom rather than draw blank filler.
func TestHeightOnlyResizeReclampsPastBottom(t *testing.T) {
	tr := New(theme.Default())
	tr.Resize(80, 21)
	for i := 0; i < 30; i++ {
		tr.Append(event(fmt.Sprint(i)))
	}
	tr.Resize(80, 18)
	tr.vp.SetYOffset(tr.vp.YOffset() - 1) // one line above the bottom
	if tr.Following() {
		t.Fatal("setup: expected to be one line above the bottom")
	}
	tr.Resize(80, 21)
	if tr.vp.PastBottom() {
		t.Errorf("growing the viewport left it past the bottom (offset %d, height %d)",
			tr.vp.YOffset(), tr.vp.Height())
	}
}
