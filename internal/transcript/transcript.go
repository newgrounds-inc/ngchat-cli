// Package transcript is the chat log: rows appended in order, drawn on
// demand, and a reader position that survives every redraw.
//
// The transcript owns its viewport (ADR 0007). A reveal, a timestamp
// toggle or a resize changes how many screen lines each row takes, so
// the reader's place is held as an anchor, a row rather than a line
// offset, read before any mutation and restored after. Keeping the
// viewport inside is what makes that ordering an invariant of this
// package instead of something every caller has to remember.
//
// Rows arrive already classified and stamped: the transcript styles
// what it is told and never reads the clock or the network. Username
// and HTML are treated as hostile and sanitized on draw; Text is
// trusted as the caller rendered it.
package transcript

import (
	"slices"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/newgrounds-inc/ngchat-cli/internal/render"
	"github.com/newgrounds-inc/ngchat-cli/internal/theme"
)

// MaxRows caps the transcript. An evening of chat must not grow without
// bound; the oldest rows are dropped first, the same loss a reconnect
// gap is.
const MaxRows = 2000

// Kind says what a row is, which decides how it is drawn.
type Kind int

const (
	// Chat is an ordinary line: a styled speaker, then the body.
	Chat Kind = iota
	// Me is a /me action. The server's HTML already leads with the
	// actor's name, as the web client relies on; only the "* " is ours.
	Me
	// Slap is drawn like Me.
	Slap
	// Server is a server-authored message: HTML drawn as an event.
	Server
	// DM is a direct message, marked "[DM]" before the speaker.
	DM
	// Event is something that happened (a join, a notice, the MOTD):
	// plain Text drawn faint.
	Event
	// Error is something that failed: plain Text in the error color.
	Error
	// Roster is the /who listing: plain Text drawn as given, since it
	// carries its own dimming for away users.
	Roster
	// Gap marks a reconnect whose backfill did not overlap what was
	// already shown; older messages are gone.
	Gap
)

// Row is one entry in the transcript. HTML is for Chat, Me, Slap,
// Server and DM and is converted on every draw so a reveal can show
// what a hidden spoiler held; Text is for Event, Error and Roster and
// must already be rendered and sanitized. At is always the caller's:
// the transcript never substitutes the current time.
type Row struct {
	Kind     Kind
	Username string
	Mod      bool
	Self     bool
	Mention  bool
	Spoiler  bool
	HTML     string
	Text     string
	At       time.Time
}

// Options are the reader's display toggles.
type Options struct {
	RevealSpoilers bool
	ShowTimes      bool
}

// styles is every role the transcript draws with, built once from the
// theme so a row names a role and never a color.
type styles struct {
	user, modUser, self, mention, dm, event, spoiler, err, time lipgloss.Style
}

func newStyles(t theme.Theme) styles {
	faint := lipgloss.NewStyle().Faint(true)
	return styles{
		user:    lipgloss.NewStyle().Bold(true).Foreground(t.Username),
		modUser: lipgloss.NewStyle().Bold(true).Foreground(t.Secondary),
		// Self is the one cool color in a warm transcript, so your own
		// lines are found at a glance.
		self: lipgloss.NewStyle().Bold(true).Foreground(t.Info),
		// Reverse on top of the colors: under NO_COLOR the renderer
		// drops the colors but keeps the attribute, so the highlight
		// still reads as a bar. Reverse swaps foreground and
		// background, so the pair is declared swapped.
		mention: lipgloss.NewStyle().Bold(true).Reverse(true).
			Foreground(t.Warning).Background(t.WarningContent),
		dm:      lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		event:   faint.Italic(true),
		spoiler: faint,
		err:     lipgloss.NewStyle().Foreground(t.Error),
		time:    faint,
	}
}

// anchor is where the reader is, in rows rather than viewport lines.
type anchor struct {
	// bottom means the reader is following the newest row; item and
	// within are then unused.
	bottom bool
	// item is the row under the top of the screen and within how many
	// lines into it the screen starts.
	item, within int
}

// Transcript is the chat log and the viewport it is read through. The
// zero value is not usable; call New. It is a value type like the
// viewport it holds, so a Bubble Tea model can carry it by value.
type Transcript struct {
	st   styles
	opts Options
	rows []Row
	// lines is every row's wrapped screen lines in order and starts the
	// index each row begins on, both valid only once sized; a row
	// appended before that is drawn on the first Resize.
	lines  []string
	starts []int
	vp     viewport.Model
	sized  bool
}

// New builds an empty transcript drawing with the theme's roles.
func New(t theme.Theme) Transcript {
	return Transcript{st: newStyles(t)}
}

// Append adds a row at the bottom. A reader following the newest row
// keeps following; one scrolled up stays on the row they were reading,
// even when the cap drops rows above it.
func (t *Transcript) Append(r Row) {
	a := t.locate()
	t.rows = append(t.rows, r)
	dropped := len(t.rows) - MaxRows
	if dropped > 0 {
		t.rows = t.rows[dropped:]
		// The anchored row moved up the slice with everything else; one
		// that fell off the cap leaves the reader on the new oldest row.
		a.item = max(0, a.item-dropped)
	}
	if !t.sized {
		return
	}
	if dropped > 0 {
		cut := t.starts[dropped]
		t.lines = t.lines[cut:]
		t.starts = t.starts[dropped:]
		for i := range t.starts {
			t.starts[i] -= cut
		}
	}
	t.starts = append(t.starts, len(t.lines))
	t.lines = append(t.lines, t.wrap(r)...)
	t.show(a)
}

// Resize fits the transcript to w columns and h rows. A new width
// re-wraps every row around the reader's anchor; a height-only change
// keeps the content and only reclamps, so a reader at the bottom stays
// there and one left past the bottom by a shrink snaps back.
func (t *Transcript) Resize(w, h int) {
	switch {
	case !t.sized:
		t.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))
		t.sized = true
		t.rebuild(anchor{bottom: true})
	case w != t.vp.Width():
		a := t.locate()
		t.vp.SetWidth(w)
		t.vp.SetHeight(h)
		t.rebuild(a)
	default:
		// SetHeight alone leaves the offset unclamped (bubbles v2.2.1).
		atBottom := t.vp.AtBottom()
		t.vp.SetHeight(h)
		if atBottom || t.vp.PastBottom() {
			t.vp.GotoBottom()
		}
	}
}

// SetOptions changes the display toggles and redraws every row around
// the reader's anchor.
func (t *Transcript) SetOptions(o Options) {
	if o == t.opts {
		return
	}
	a := t.locate()
	t.opts = o
	t.rebuild(a)
}

// Options reports the current display toggles.
func (t Transcript) Options() Options { return t.opts }

// PageUp scrolls back one screen; the reader stops following.
func (t *Transcript) PageUp() { t.vp.PageUp() }

// PageDown scrolls forward one screen.
func (t *Transcript) PageDown() { t.vp.PageDown() }

// Follow jumps to the newest row and keeps the reader there.
func (t *Transcript) Follow() { t.vp.GotoBottom() }

// Following reports whether the reader is at the bottom. Before the
// first Resize there is nothing to be scrolled up in, so it is true.
func (t Transcript) Following() bool { return t.vp.AtBottom() }

// Height is the viewport's row count as of the last Resize.
func (t Transcript) Height() int { return t.vp.Height() }

// Rows is what the transcript holds, oldest first. The slice is the
// transcript's own; callers read it and do not keep it.
func (t Transcript) Rows() []Row { return t.rows }

// View draws the visible screen lines, or nothing before the first
// Resize.
func (t Transcript) View() string {
	if !t.sized {
		return ""
	}
	return t.vp.View()
}

// locate reads the anchor off the viewport as it stands. Every mutation
// calls it first and hands the result to show.
func (t Transcript) locate() anchor {
	if !t.sized || t.vp.AtBottom() || len(t.starts) == 0 {
		return anchor{bottom: true}
	}
	y := t.vp.YOffset()
	i := sort.Search(len(t.starts), func(i int) bool {
		return t.starts[i] > y
	}) - 1
	if i < 0 {
		return anchor{bottom: true}
	}
	return anchor{item: i, within: y - t.starts[i]}
}

// rebuild re-wraps every row and puts the reader back at a.
func (t *Transcript) rebuild(a anchor) {
	t.lines = t.lines[:0]
	t.starts = t.starts[:0]
	for _, r := range t.rows {
		t.starts = append(t.starts, len(t.lines))
		t.lines = append(t.lines, t.wrap(r)...)
	}
	t.show(a)
}

// show hands the lines to the viewport and restores a: the bottom, or
// the same row at the top of the screen. within is clamped to the
// row's new height, so a row that shrank (a spoiler hidden again) does
// not push the screen into the row after it.
func (t *Transcript) show(a anchor) {
	// The viewport keeps the slice it is given, so it gets its own copy
	// and later appends here cannot reach into it.
	t.vp.SetContentLines(slices.Clone(t.lines))
	if a.bottom || a.item >= len(t.starts) {
		t.vp.GotoBottom()
		return
	}
	end := len(t.lines)
	if a.item+1 < len(t.starts) {
		end = t.starts[a.item+1]
	}
	within := min(a.within, end-t.starts[a.item]-1)
	t.vp.SetYOffset(t.starts[a.item] + within)
}

// wrap draws one row and splits it into screen lines at the viewport
// width.
func (t Transcript) wrap(r Row) []string {
	w := lipgloss.NewStyle().Width(t.vp.Width())
	return strings.Split(w.Render(t.draw(r)), "\n")
}

// draw renders one row as a single styled string.
func (t Transcript) draw(r Row) string {
	prefix := ""
	if t.opts.ShowTimes {
		prefix = t.st.time.Render(r.At.Local().Format("15:04")) + " "
	}
	switch r.Kind {
	case Gap:
		return prefix + t.st.event.Render("— reconnected, older messages missing —")
	case Event:
		return prefix + t.st.event.Render(r.Text)
	case Error:
		return prefix + t.st.err.Render(r.Text)
	case Roster:
		return prefix + r.Text
	case Server:
		return prefix + t.st.event.Render(render.Text(r.HTML))
	case Me, Slap:
		return prefix + t.st.event.Render("* "+render.Text(r.HTML))
	}

	name := t.st.user
	if r.Mod {
		name = t.st.modUser
	}
	if r.Self {
		name = t.st.self
	}
	if r.Mention {
		name = t.st.mention
	}
	speaker := name.Render("<" + render.Line(r.Username) + ">")
	if r.Kind == DM {
		speaker = t.st.dm.Render("[DM] ") + speaker
	}
	body := render.Text(r.HTML)
	if r.Spoiler && !t.opts.RevealSpoilers {
		body = t.st.spoiler.Render("▒▒▒ spoiler — ctrl+s to reveal ▒▒▒")
	}
	return prefix + speaker + " " + body
}
