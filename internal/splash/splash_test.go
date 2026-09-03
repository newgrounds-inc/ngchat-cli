package splash

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/newgrounds-inc/ngchat-cli/internal/theme"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(lines []string) string {
	return ansi.ReplaceAllString(strings.Join(lines, "\n"), "")
}

func TestWordmarkShape(t *testing.T) {
	a := Wordmark()
	if a.W != 38 || a.H != 7 || a.Rows() != 4 {
		t.Fatalf("wordmark is %d×%d px, %d rows; want 38×7, 4",
			a.W, a.H, a.Rows())
	}
	// The static render is the bitmap itself, top row of each cell
	// pair: a wrong glyph table shows up here as a wrong picture.
	want := strings.Join([]string{
		"█▄  █ ▄▀▀▀▄    ▄▀▀▀▄ █   █ ▄▀▀▀▄ ▀▀█▀▀",
		"█▀▄ █ █ ▄▄▄    █     █▄▄▄█ █▄▄▄█   █  ",
		"█  ██ █   █    █   ▄ █   █ █   █   █  ",
		"▀   ▀  ▀▀▀      ▀▀▀  ▀   ▀ ▀   ▀   ▀  ",
	}, "\n")
	if got := plain(Static(a, FromTheme(theme.Default()))); got != want {
		t.Errorf("static wordmark:\n%s\nwant:\n%s", got, want)
	}
}

func TestScaleAndFit(t *testing.T) {
	a := Wordmark()
	cases := []struct {
		w, h  int
		scale int // 0 means does not fit
	}{
		{200, 50, 4},
		{120, 12, 3},
		{114, 11, 3},
		{113, 11, 2},
		{80, 24, 2},
		{76, 7, 2},
		{76, 6, 1},
		{40, 4, 1},
		{37, 24, 0},
		{80, 3, 0},
	}
	for _, c := range cases {
		s, ok := Fit(a, c.w, c.h)
		if ok != (c.scale > 0) {
			t.Errorf("Fit(%d×%d) ok=%v, want %v", c.w, c.h, ok, c.scale > 0)
			continue
		}
		if ok && s.W != a.W*c.scale {
			t.Errorf("Fit(%d×%d) scale %d, want %d", c.w, c.h, s.W/a.W, c.scale)
		}
	}
	s := a.Scale(2)
	for y := range s.H {
		for x := range s.W {
			if s.At(x, y) != a.At(x/2, y/2) {
				t.Fatalf("Scale(2) pixel (%d,%d) differs from source", x, y)
			}
		}
	}
}

// TestFrameGeometry: every frame is exactly the art's rows and width,
// whatever the instant, so the UI can center it without measuring.
func TestFrameGeometry(t *testing.T) {
	a := Wordmark().Scale(2)
	p := FromTheme(theme.Default())
	l := DefaultLaser
	for _, at := range []time.Duration{-1, 0, 10 * time.Millisecond,
		l.Sweep / 2, l.Sweep, l.Sweep + l.Cool/2, l.Duration(), time.Hour} {
		lines := l.Frame(a, p, at)
		if len(lines) != a.Rows() {
			t.Fatalf("t=%s: %d lines, want %d", at, len(lines), a.Rows())
		}
		for i, line := range lines {
			if w := lipgloss.Width(line); w != a.W {
				t.Errorf("t=%s line %d is %d cells wide, want %d", at, i, w, a.W)
			}
		}
	}
}

func TestLaserRevealsLeftToRight(t *testing.T) {
	a := Wordmark()
	p := FromTheme(theme.Default())
	l := DefaultLaser
	static := Static(a, p)
	rowRunes := func(lines []string, i int) []rune {
		return []rune(ansi.ReplaceAllString(lines[i], ""))
	}

	// At t=0 the beam sits on column 0, which is revealed with it;
	// nothing to its right is.
	for i, line := range l.Frame(a, p, 0) {
		if rest := string(rowRunes([]string{line}, 0)[1:]); strings.ContainsAny(rest, "█▀▄") {
			t.Errorf("t=0 row %d shows art past column 0: %q", i, rest)
		}
	}

	mid := l.Frame(a, p, l.Sweep/2)
	half := a.W / 2
	for i := range mid {
		got, want := rowRunes(mid, i), rowRunes(static, i)
		if string(got[:half]) != string(want[:half]) {
			t.Errorf("row %d left half at mid-sweep differs from static:\n%q\n%q",
				i, string(got[:half]), string(want[:half]))
		}
		// The beam is at half; sparks reach three columns past it.
		if ahead := string(got[half+4:]); strings.ContainsAny(ahead, "█▀▄") {
			t.Errorf("row %d shows art ahead of the beam: %q", i, ahead)
		}
	}

	done := plain(l.Frame(a, p, l.Duration()))
	if want := plain(static); done != want {
		t.Errorf("frame at Duration is not static:\n%s", done)
	}
	if strings.ContainsAny(done, "│·") {
		t.Error("beam or sparks survive past Duration")
	}
}

// TestFrameIsDeterministic: sparks are hashed from the frame index, so
// the same instant renders the same bytes. The UI relies on that only
// loosely, but a flaky splash test would be worse than none.
func TestFrameIsDeterministic(t *testing.T) {
	a := Wordmark()
	p := FromTheme(theme.Default())
	at := 400 * time.Millisecond
	x, y := DefaultLaser.Frame(a, p, at), DefaultLaser.Frame(a, p, at)
	if strings.Join(x, "\n") != strings.Join(y, "\n") {
		t.Error("two renders of the same instant differ")
	}
}

func TestMix(t *testing.T) {
	black, white := lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	cases := []struct {
		t    float64
		want string
	}{
		{-1, "#000000"}, {0, "#000000"}, {0.5, "#808080"}, {1, "#ffffff"},
		{2, "#ffffff"},
	}
	for _, c := range cases {
		got := mix(black, white, c.t)
		if !sameColor(got, lipgloss.Color(c.want)) {
			t.Errorf("mix(black, white, %v) = %v, want %s", c.t, got, c.want)
		}
	}
}
