// Package ui is the fullscreen Bubble Tea interface: status bar, message
// viewport, input line. Scrollback lives in the viewport (not the
// terminal's), so paging keys and spoiler re-rendering are handled here.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/complete"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
	"github.com/newgrounds-inc/ngchat-cli/internal/render"
	"github.com/newgrounds-inc/ngchat-cli/internal/splash"
	"github.com/newgrounds-inc/ngchat-cli/internal/theme"
)

const (
	maxMessageLength = 5000 // server-side limit per chat message
	// maxItems caps the transcript. The viewport re-renders every row on
	// each change, so an evening of chat must not grow without bound; the
	// oldest rows are dropped first, the same loss a reconnect gap is.
	maxItems = 2000
	// maxNotices bounds how much of the server's away-inbox is replayed
	// on the first subscribe. The inbox holds up to 100 rows and the
	// server never clears it, so showing all of it would bury the live
	// channel under stale pings every start.
	maxNotices = 10
)

const (
	// frameRate paces the splash; 30 fps is smooth for a sweep and
	// well under what a remote terminal chokes on.
	frameRate = 33 * time.Millisecond
	// maxSplash bounds how long the finished animation holds while the
	// client is still connecting. Past it the chat screen's own
	// "connecting…" status is more honest than a logo.
	maxSplash = 4 * time.Second
)

// styles is every lipgloss style the screen uses, built once from the
// theme. Widgets name a role (user, mod, mention), never a color, so a
// theme swap touches nothing below this.
type styles struct {
	status, user, modUser, self, mention, dm, event, spoiler, err,
	help, time, away, pick lipgloss.Style
}

// newStyles maps theme roles onto the transcript. The status bar and
// the mention highlight carry Reverse on top of their colors: under
// NO_COLOR the renderer drops the colors but keeps the attribute, so
// both still read as a bar. (TERM=dumb is the NoTTY profile, which
// drops every attribute too; there they are plain text.) Reverse swaps
// foreground and background, so those two pairs are declared swapped.
func newStyles(t theme.Theme) styles {
	faint := lipgloss.NewStyle().Faint(true)
	return styles{
		status: lipgloss.NewStyle().Reverse(true).Padding(0, 1).
			Foreground(t.Primary).Background(t.PrimaryContent),
		user:    lipgloss.NewStyle().Bold(true).Foreground(t.Username),
		modUser: lipgloss.NewStyle().Bold(true).Foreground(t.Secondary),
		// Self is the one cool color in a warm transcript, so your own
		// lines are found at a glance.
		self: lipgloss.NewStyle().Bold(true).Foreground(t.Info),
		mention: lipgloss.NewStyle().Bold(true).Reverse(true).
			Foreground(t.Warning).Background(t.WarningContent),
		dm:      lipgloss.NewStyle().Bold(true).Foreground(t.Accent),
		event:   faint.Italic(true),
		spoiler: faint,
		err:     lipgloss.NewStyle().Foreground(t.Error),
		help:    faint,
		time:    faint,
		away:    faint,
		// pick highlights the selected completion row the same way the
		// status bar reads as a bar under NO_COLOR: color plus Reverse,
		// so the attribute survives where the color does not.
		pick: lipgloss.NewStyle().Reverse(true).
			Foreground(t.Primary).Background(t.PrimaryContent),
	}
}

// item is one rendered row of the transcript.
type item struct {
	kind     string // "chat", "me", "slap", "server", "dm", "event", "gap"
	username string
	isMod    bool
	html     string // server-rendered HTML, converted lazily on render
	spoiler  bool
	mention  bool   // addressed to self: highlighted, and rang the bell
	text     string // pre-rendered text for non-chat rows
	at       time.Time
}

// typingState remembers who is composing and when they last said so.
type typingState struct {
	username string
	at       time.Time
}

// chatConn is the seam over the network client that sending goes
// through, so tests can inject a fake and observe what was sent
// without a real socket. *client.Client satisfies it.
type chatConn interface {
	SendChat(text string) error
	SendTyping()
}

// completionState is the open completion list, or nil when none is
// open. index, top and end are all in terms of the candidates and span
// of res, which is frozen until the next recompute.
type completionState struct {
	res complete.Result
	// index is the highlighted candidate.
	index int
	// top is the first visible candidate row, kept so index always
	// stays inside the drawn window.
	top int
	// end is the current end of the span in the line: normally
	// res.End, but no-list mode advances it as cycling writes and
	// replaces a preview in place.
	end int
	// previewed is no-list mode's memory that a preview has already
	// been written once, so the very first navigation key can *show*
	// the already-selected candidate (index 0) without first *moving*
	// past it the way a visible list's first tab does — the list mode
	// highlight is visible from the moment it opens, so its first tab
	// has something to move away from; a no-list open has shown
	// nothing yet.
	previewed bool
}

// Options configures the UI beyond the client it wraps.
type Options struct {
	// Channel is the name shown in the status bar and placeholder.
	Channel string
	// Quiet suppresses the terminal bell on mentions and DMs.
	Quiet bool
	// Theme colors the screen; the zero value means theme.Default().
	Theme theme.Theme
	// Splash is the opening animation; nil plays none.
	Splash splash.Effect
}

// splashState is the opening animation in flight. The clock is the
// tea.Tick timestamps, not time.Now, so a test can drive it to any
// instant.
type splashState struct {
	effect  splash.Effect
	palette splash.Palette
	base    splash.Art // the wordmark at scale 1
	art     splash.Art // base fitted to the terminal; zero until sized
	fits    bool
	start   time.Time
	elapsed time.Duration
}

// Model is the Bubble Tea root model.
type Model struct {
	chat    *client.Client
	conn    chatConn // chat behind the seam tests can fake; nil if chat is nil
	channel string
	quiet   bool
	bell    io.Writer // where the BEL byte goes; the terminal in production

	// completion is the open completion list, nil when closed.
	completion *completionState
	engine     complete.Engine
	// sources is nil in production: completionSources builds the real
	// list fresh on every call instead (see its doc comment for why).
	// A test that sets this field non-nil overrides that and gets a
	// fixed list back every time.
	sources []complete.Source

	items          []item
	revealSpoilers bool
	showTimes      bool
	state          client.State
	stopErr        error
	retryErr       error // why the last session ended, while reconnecting
	self           string
	// isAdmin and isChatMod gate the command completion list; both are
	// set from Authenticated and kept current by Revalidated, since a
	// renewal's flags supersede the ones the session started with.
	isAdmin, isChatMod bool
	motd               string // server HTML from Authenticated, shown after each backfill
	typing             map[string]typingState
	// users is the roster keyed by user ID, seeded by Subscribed and
	// patched by userJoined, userLeft, userUpdated and away.
	users map[int]protocol.ChannelUser
	// noticed records that the first subscribe already replayed the
	// away-inbox; reconnects carry the same rows again and must not.
	noticed bool

	vp    viewport.Model
	input textinput.Model
	ready bool

	st            styles
	width, height int
	// splash is non-nil only while the opening animation plays.
	splash *splashState
}

// New builds the UI over a running chat client.
func New(chat *client.Client, opts Options) Model {
	input := textinput.New()
	input.Placeholder = "message #" + opts.Channel
	input.CharLimit = maxMessageLength
	input.Focus()
	th := opts.Theme
	if th.Name == "" {
		th = theme.Default()
	}
	m := Model{
		chat:    chat,
		channel: opts.Channel,
		quiet:   opts.Quiet,
		bell:    os.Stdout,
		input:   input,
		typing:  map[string]typingState{},
		users:   map[int]protocol.ChannelUser{},
		st:      newStyles(th),
	}
	// A nil *client.Client stored in the chatConn interface would not
	// compare equal to nil (a typed nil is a non-nil interface), so the
	// guard has to happen here rather than in every send site.
	if chat != nil {
		m.conn = chat
	}
	if opts.Splash != nil {
		m.splash = &splashState{
			effect:  opts.Splash,
			palette: splash.FromTheme(th),
			base:    splash.Wordmark(),
			start:   time.Now(),
		}
	}
	return m
}

// tickMsg drives typing-indicator expiry.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// frameMsg advances the splash. Frames are scheduled one at a time,
// like tick, so ending the splash stops the clock with it.
type frameMsg time.Time

func frame() tea.Cmd {
	return tea.Tick(frameRate, func(t time.Time) tea.Msg {
		return frameMsg(t)
	})
}

// waitEvent pumps the next client event into the Bubble Tea loop.
func waitEvent(ch <-chan client.Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return e
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{waitEvent(m.chat.Events()), tick(), textinput.Blink}
	if m.splash != nil {
		cmds = append(cmds, frame())
	}
	return tea.Batch(cmds...)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout(msg.Width, msg.Height)
		m.fitSplash()
		return m, nil

	case frameMsg:
		if m.splash == nil {
			return m, nil
		}
		m.splash.elapsed = time.Time(msg).Sub(m.splash.start)
		if m.splashOver() {
			m.endSplash()
			return m, nil
		}
		return m, frame()

	case tea.KeyPressMsg:
		// Any key skips the splash and is spent doing so; only quit
		// passes through, so ctrl+c never has to be pressed twice.
		if m.splash != nil && msg.String() != "ctrl+c" {
			m.endSplash()
			return m, nil
		}
		if m.completion != nil && m.updateCompletion(msg) {
			return m, nil
		}
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "tab", "shift+tab":
			// Reaching here means no completion is open (an open one is
			// handled above), and tab carries no printable Text, so the
			// textinput would insert nothing — but SendTyping still
			// fired for it. Consume it as a true no-op instead.
			return m, nil
		case "ctrl+s":
			m.revealSpoilers = !m.revealSpoilers
			m.refresh(false)
			return m, nil
		case "ctrl+t":
			m.showTimes = !m.showTimes
			m.refresh(false)
			return m, nil
		case "pgup", "pgdown":
			// Only the paging keys reach the viewport: its default keymap
			// also binds letters, which belong to the input line.
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case "enter":
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			if isWho(text) {
				m.push(item{kind: "event", text: m.whoText()})
				m.input.Reset()
				m.recompute()
				return m, nil
			}
			if m.conn == nil {
				m.push(item{kind: "event",
					text: m.st.err.Render("send failed: not connected")})
				return m, nil
			}
			if err := m.conn.SendChat(text); err != nil {
				m.push(item{kind: "event",
					text: m.st.err.Render("send failed: " + err.Error())})
				return m, nil
			}
			m.input.Reset()
			m.recompute()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.conn != nil && m.input.Value() != "" {
			m.conn.SendTyping()
		}
		m.recompute()
		return m, cmd

	case tickMsg:
		for name, t := range m.typing {
			if time.Since(t.at) > 4*time.Second {
				delete(m.typing, name)
			}
		}
		return m, tick()

	case client.Event:
		m.handleEvent(msg)
		if m.leaves() {
			// Nothing inside the TUI can fix a revoked remember cookie
			// (no inline re-login) or an entry-gate refusal, so leave
			// the screen and let main print the one useful line.
			return m, tea.Quit
		}
		return m, waitEvent(m.chat.Events())
	}
	return m, nil
}

// fitSplash sizes the wordmark to the terminal, leaving room for the
// two caption lines and a margin. A terminal too small for scale 1 gets
// no splash rather than a clipped one.
func (m *Model) fitSplash() {
	if m.splash == nil {
		return
	}
	art, ok := splash.Fit(m.splash.base, m.width-2, m.height-4)
	if !ok {
		m.endSplash()
		return
	}
	m.splash.art, m.splash.fits = art, true
}

// splashOver is when the animation has finished and the client is
// online, or has finished and waited maxSplash for it.
func (m Model) splashOver() bool {
	s := m.splash
	if s.elapsed < s.effect.Duration() {
		return false
	}
	return m.state == client.StateOnline || s.elapsed >= maxSplash
}

// endSplash hands the screen to the chat layout, which has been kept
// current underneath all along.
func (m *Model) endSplash() {
	m.splash = nil
	m.refresh(false)
}

// SignedOut reports whether the client stopped because the site refused
// the remember cookie. main reads it after the program exits.
func (m Model) SignedOut() bool {
	return m.state == client.StateStopped &&
		errors.Is(m.stopErr, auth.ErrSignedOut)
}

// Denied returns the server's entry-gate notice (supporter-only, under
// 18, e-mail not validated, banned) as terminal text when that is why
// the client stopped, else "". main prints it after the program exits:
// the notice is the whole answer and nothing in the TUI can act on it.
func (m Model) Denied() string {
	var denied *client.AccessDenied
	if m.state == client.StateStopped && errors.As(m.stopErr, &denied) {
		return render.Text(denied.Message)
	}
	return ""
}

// leaves reports whether the stop is one the TUI should exit on rather
// than sit disconnected: the user can do nothing about it from inside.
func (m Model) leaves() bool {
	return m.SignedOut() || m.Denied() != ""
}

// isWho matches the local /who command and nothing else; every other
// slash command goes to the server, which parses them.
func isWho(text string) bool {
	return strings.EqualFold(text, "/who")
}

// handleEvent folds a chat-client event into the transcript.
func (m *Model) handleEvent(e client.Event) {
	m.state = e.State
	switch e.State {
	case client.StateStopped:
		m.stopErr = e.Err
		// The status bar is one truncated line; the transcript row is
		// where the full reason can be read.
		if e.Err != nil {
			m.pushEvent(m.st.err.Render("disconnected: "+render.Line(e.Err.Error())), 0)
		}
	case client.StateReconnecting:
		m.retryErr = e.Err
	}
	if e.Gap {
		m.push(item{kind: "gap", at: time.Now()})
	}
	switch msg := e.Msg.(type) {
	case protocol.Authenticated:
		m.self = msg.Username
		m.isAdmin = msg.IsAdmin
		m.isChatMod = msg.IsChatMod
		m.motd = msg.MOTDText()
	case client.BackfillDone:
		// Below the history so it is the first thing read on join, like
		// the site's banner, rather than scrolled off by the backfill.
		if m.motd != "" {
			m.pushEvent(m.st.event.Render(render.Text(m.motd)), 0)
		}
	case protocol.Subscribed:
		m.users = make(map[int]protocol.ChannelUser, len(msg.UserList))
		for _, u := range msg.UserList {
			m.users[u.UserID] = u
		}
		if !m.noticed {
			m.noticed = true
			m.pushNotices(msg.Notifications)
		}
	case protocol.Message:
		m.pushMessage(msg, e.Backfill)
	case protocol.TypingEvent:
		if name := render.Line(msg.Username); name != "" && name != m.self {
			m.typing[name] = typingState{name, time.Now()}
		}
	case protocol.UserJoined:
		m.users[msg.UserID] = rosterEntry(msg)
		m.pushEvent(m.st.event.Render(render.Line(msg.Username)+" joined"), msg.ServerTime)
	case protocol.UserUpdated:
		// A silent roster patch: never a join notice (ADR 0002 drift note
		// on the upstream schema).
		m.users[msg.UserID] = rosterEntry(protocol.UserJoined(msg))
	case protocol.UserLeft:
		delete(m.users, msg.UserID)
		m.pushEvent(m.st.event.Render(render.Line(msg.Username)+" left"), msg.ServerTime)
		delete(m.typing, render.Line(msg.Username))
	case protocol.Away:
		u, known := m.users[msg.UserID]
		if !known {
			u = protocol.ChannelUser{UserID: msg.UserID, Username: msg.Username,
				IsAdmin: msg.IsAdmin, IsChatMod: msg.IsChatMod}
		}
		u.IsAway = msg.IsAway
		u.AwayMessage = msg.AwayMessage
		u.AwayMessageRaw = msg.AwayMessageRaw
		m.users[msg.UserID] = u
		m.pushEvent(m.awayText(msg), msg.ServerTime)
	case protocol.Revalidated:
		// The socket renewed its token in place; the flags on this
		// message now describe self. They matter for the command
		// completion list, which hides commands above the viewer's
		// rank: a mod promoted or demoted mid-session sees the change
		// on the next "/" without reconnecting.
		m.isAdmin = msg.IsAdmin
		m.isChatMod = msg.IsChatMod
	case client.RenewalFailed:
		m.pushEvent(m.st.event.Render(
			"token renewal failed ("+render.Line(msg.Err.Error())+
				"); will reconnect when the current token expires"), 0)
	case protocol.Unauthorized:
		// Only reaches the UI after authentication, right before the
		// server closes the socket (expiry or a rejected renewal).
		m.pushEvent(m.st.event.Render(render.Line(msg.Message)), 0)
	case protocol.Error:
		m.pushEvent(m.st.err.Render(render.Line(msg.Message)), 0)
	}
}

// rosterEntry converts a join (or update) frame into a roster row.
func rosterEntry(u protocol.UserJoined) protocol.ChannelUser {
	return protocol.ChannelUser{
		UserID:         u.UserID,
		Username:       u.Username,
		IsAdmin:        u.IsAdmin,
		IsChatMod:      u.IsChatMod,
		IsAway:         u.IsAway,
		AwayMessage:    u.AwayMessage,
		AwayMessageRaw: u.AwayMessageRaw,
		UserIcon:       u.UserIcon,
		UserPageURL:    u.UserPageURL,
	}
}

// awayText phrases an away frame as an event row.
func (m Model) awayText(a protocol.Away) string {
	name := render.Line(a.Username)
	if !a.IsAway {
		return m.st.event.Render(name + " is back")
	}
	text := name + " is away"
	if a.AwayMessage != "" {
		text += ": " + render.Text(a.AwayMessage)
	}
	return m.st.event.Render(text)
}

// pushNotices replays the newest rows of the away-inbox, oldest first so
// the transcript reads downward, as faint event rows.
func (m *Model) pushNotices(notes []protocol.Notification) {
	if len(notes) > maxNotices {
		notes = notes[:maxNotices]
	}
	for i := len(notes) - 1; i >= 0; i-- {
		n := notes[i]
		who := render.Line(n.Username)
		if who == "" {
			who = "someone"
		}
		label := "while you were away · " + who + ": "
		if n.MessageType == "directMessage" || n.MessageType == "modDirectMessage" {
			label = "while you were away · [DM] " + who + ": "
		}
		m.pushEvent(m.st.event.Render(label+render.Text(n.Message)), n.ServerTime)
	}
}

// pushEvent appends a pre-rendered event row stamped with the server's
// time, or now when the frame carries none.
func (m *Model) pushEvent(text string, serverTime int64) {
	m.push(item{kind: "event", text: text, at: stamp(serverTime)})
}

// stamp converts a server millisecond timestamp, falling back to now.
func stamp(serverTime int64) time.Time {
	if serverTime == 0 {
		return time.Now()
	}
	return time.UnixMilli(serverTime)
}

// pushMessage classifies a wire message into a transcript item. A message
// addressed to self is highlighted; a live one also rings the bell, while
// a backfilled one does not, since it is old news every reconnect.
func (m *Model) pushMessage(msg protocol.Message, backfill bool) {
	kind := "chat"
	switch msg.Name {
	case "meMessage":
		kind = "me"
	case "slapMessage":
		kind = "slap"
	case "serverMessage":
		kind = "server"
	case "directMessage":
		kind = "dm"
	}
	delete(m.typing, render.Line(msg.Username))
	mention := m.addressedToMe(msg)
	if mention && !backfill && !m.quiet {
		_, _ = io.WriteString(m.bell, "\a")
	}
	m.push(item{
		kind:     kind,
		username: msg.Username,
		isMod:    msg.IsAdmin || msg.IsChatMod,
		html:     msg.Message,
		spoiler:  msg.IsSpoiler,
		mention:  mention,
		at:       stamp(msg.ServerTime),
	})
}

// addressedToMe reports whether a message is a DM to self or names self
// in its mentions. The server echoes our own DMs back, so those and
// anything else from self never count. Mentions arrive as the server's
// formatter captured them: lowercased and with the leading "@" kept
// ("@bob"); a group mention ("@!everyone") keeps its "!" and so never
// matches a username.
func (m *Model) addressedToMe(msg protocol.Message) bool {
	if m.self == "" || strings.EqualFold(msg.Username, m.self) {
		return false
	}
	if msg.Name == "directMessage" {
		return true
	}
	for _, name := range msg.Mentions {
		if strings.EqualFold(strings.TrimPrefix(name, "@"), m.self) {
			return true
		}
	}
	return false
}

// push appends a transcript item and re-renders, following the bottom
// unless the user has scrolled up.
func (m *Model) push(it item) {
	if it.at.IsZero() {
		it.at = time.Now()
	}
	m.items = append(m.items, it)
	if len(m.items) > maxItems {
		m.items = m.items[len(m.items)-maxItems:]
	}
	m.refresh(true)
}

// layout applies terminal dimensions to the widgets. It also records
// w and h on the model itself: listRows needs the current height even
// when a test calls layout directly rather than through a
// tea.WindowSizeMsg.
func (m *Model) layout(w, h int) {
	m.width, m.height = w, h
	vh := m.viewportHeight()
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(vh))
		m.ready = true
	} else {
		m.vp.SetWidth(w)
		m.vp.SetHeight(vh)
	}
	m.input.SetWidth(max(10, w-4))
	m.refresh(false)
	// A resize can change listRows (more or fewer rows to spare) without
	// the candidate count changing, which is exactly the case
	// cycleCompletion's own window math never had to handle before: top
	// must be reclamped or a shrink can leave the highlight outside the
	// drawn window, and a later grow can walk completionView's loop past
	// the end of Candidates.
	m.clampWindow()
}

// viewportHeight is the transcript's row count for the model's current
// height: the fixed chrome (status, input, help) plus whatever the
// open completion list currently reserves. Shared by layout and
// relayout so the two never drift into computing it differently.
func (m Model) viewportHeight() int {
	const chrome = 1 + 1 + 1 // status + input + help
	return max(1, m.height-chrome-m.listHeight())
}

// relayout resizes the viewport for the current completion state
// without losing the reader's scroll position. SetHeight alone leaves
// the content unclamped, and refresh(false) jumps to the bottom on
// every call, which would fight a reader who scrolled up right as the
// list opened or closed underneath them: it is called only when the
// list changes, so GotoBottom only fires if the reader was already at
// the bottom — or if shrinking the viewport left it scrolled past the
// bottom, which SetHeight does not reclamp on its own (bubbles v2.2.1).
func (m *Model) relayout() {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	m.vp.SetHeight(m.viewportHeight())
	if atBottom || m.vp.PastBottom() {
		m.vp.GotoBottom()
	}
}

// listRows is how many candidate rows the open completion list gets.
// budget is the room left for a list at all once the fixed chrome
// (status, input, help) and a 3-row floor for the transcript are taken
// out; below 3 there is not enough to spare and no list is drawn at
// all (no-list mode — see noList), however few candidates there are.
// At or above it, the list still takes at most 5 rows and never more
// than there are candidates.
func (m Model) listRows() int {
	if m.completion == nil {
		return 0
	}
	budget := m.height - 3 - 3
	if budget < 3 {
		return 0
	}
	n := len(m.completion.res.Candidates)
	if n > 5 {
		n = 5
	}
	if n > budget {
		n = budget
	}
	return n
}

// listHeight is the rows the list (header plus candidate rows) takes
// from the viewport, or 0 when nothing is drawn.
func (m Model) listHeight() int {
	if n := m.listRows(); n >= 1 {
		return n + 1
	}
	return 0
}

// noList reports whether completion is open but the terminal is too
// small to draw a list: cycling still works, but as an in-line preview
// rather than a dropdown (see cycleCompletion).
func (m Model) noList() bool {
	return m.completion != nil && m.listRows() < 1
}

// clampWindow keeps the completion window's top valid whenever
// listRows or the candidate count might have changed size out from
// under it — a terminal resize while scrolled through a long list is
// the main case, since layout used to leave top untouched across one.
// top is clamped into [0, len(candidates)-rows], then nudged so index
// still falls inside [top, top+rows); with no list open, or no room
// for one, top is reset to 0 since it means nothing. Called from
// layout (after a resize) and cycleCompletion (after every move);
// completionView never calls it — drawing the list must stay
// read-only, and every path that can invalidate top already reclamps
// it before a draw could see it.
func (m *Model) clampWindow() {
	cs := m.completion
	if cs == nil {
		return
	}
	rows := m.listRows()
	if rows < 1 {
		cs.top = 0
		return
	}
	maxTop := len(cs.res.Candidates) - rows
	if maxTop < 0 {
		maxTop = 0
	}
	if cs.top > maxTop {
		cs.top = maxTop
	} else if cs.top < 0 {
		cs.top = 0
	}
	if cs.index < cs.top {
		cs.top = cs.index
	} else if cs.index >= cs.top+rows {
		cs.top = cs.index - rows + 1
	}
}

// byteOffset converts a rune index (what textinput.Position returns)
// to a byte offset into s (what complete.Engine and Source consume).
func byteOffset(s string, runeIdx int) int {
	runes := []rune(s)
	if runeIdx < 0 {
		runeIdx = 0
	} else if runeIdx > len(runes) {
		runeIdx = len(runes)
	}
	return len(string(runes[:runeIdx]))
}

// runeOffset converts a byte offset into s to a rune index (what
// textinput.SetCursor consumes). byteIdx is expected to fall on a rune
// boundary, which every offset this package produces does: they come
// from len() of whole inserted strings, never a raw cursor guess.
func runeOffset(s string, byteIdx int) int {
	if byteIdx > len(s) {
		byteIdx = len(s)
	} else if byteIdx < 0 {
		byteIdx = 0
	}
	return len([]rune(s[:byteIdx]))
}

// completionSources returns the sources completion should query this
// call. Model is a value type Bubble Tea copies on every Update, so a
// slice built once in New and closed over that copy would read that
// copy's self forever, and its users forever too: Subscribed replaces
// m.users wholesale with a new map (see its handleEvent case), so the
// captured copy's map would stay the empty one New made and never see
// a roster at all. Building the slice fresh from the current receiver
// on every call sidesteps both. Tests bypass all of this by setting
// sources directly, which this returns unchanged when non-nil.
func (m *Model) completionSources() []complete.Source {
	if m.sources != nil {
		return m.sources
	}
	return []complete.Source{
		complete.Mentions{Users: m.rosterUsers, Self: m.selfName},
		complete.Commands{Viewer: m.viewerAccess},
	}
}

// viewerAccess reports self's command rank from the privilege flags
// Authenticated and Revalidated keep current, for complete.Commands to
// filter against. It is a method (not a captured value) so each
// completionSources call reads whatever Revalidated last set.
func (m *Model) viewerAccess() complete.Access {
	return complete.AccessFor(m.isAdmin, m.isChatMod)
}

// rosterUsers adapts the roster to what complete.Mentions needs, read
// fresh at query time so a join, a leave or an away change between
// keystrokes needs no change notification into the completion package.
// Usernames are sanitized here with render.Line; completionRow
// sanitizes again when it draws the label, which is harmless since
// render.Line is idempotent on already-clean text. A username that
// sanitizes to "" (all control/bidi characters, or an empty one off
// the wire) is dropped rather than offered as a blank row that would
// splice "@ " on accept.
func (m *Model) rosterUsers() []complete.User {
	users := make([]complete.User, 0, len(m.users))
	for _, u := range m.users {
		if name := render.Line(u.Username); name != "" {
			users = append(users, complete.User{Name: name, Away: u.IsAway})
		}
	}
	return users
}

// selfName reports the signed-in username, or "" before Authenticated
// arrives, sanitized with render.Line so it compares against the same
// form rosterUsers produces for the roster's own copy of that name.
func (m *Model) selfName() string { return render.Line(m.self) }

// recompute re-evaluates completion from the current line and cursor.
// It runs on every keystroke that reaches the input, but never while an
// open list is being navigated: updateCompletion returns before this is
// reached for tab/shift+tab/up/down/esc, which is what keeps the
// candidate set from changing out from under the highlighted index.
func (m *Model) recompute() {
	value := m.input.Value()
	cursor := byteOffset(value, m.input.Position())
	res, ok := m.engine.Complete(value, cursor, m.completionSources())
	wasOpen := m.completion != nil
	prevN := 0
	if wasOpen {
		prevN = len(m.completion.res.Candidates)
	}
	if !ok {
		m.completion = nil
		if wasOpen {
			m.relayout()
		}
		return
	}
	m.completion = &completionState{res: res, index: 0, top: 0, end: res.End}
	if !wasOpen || prevN != len(res.Candidates) {
		m.relayout()
	}
}

// updateCompletion applies a key to an open completion list and reports
// whether it did: ctrl+c and every key besides the ones below report
// false so the caller falls through to its own handling (ctrl+c must
// still quit with a list open). Navigation and dismissal never touch
// the network; only accept does, and never sends the line itself.
func (m *Model) updateCompletion(msg tea.KeyPressMsg) bool {
	cs := m.completion
	switch msg.String() {
	case "tab":
		if len(cs.res.Candidates) == 1 {
			m.acceptCompletion()
		} else {
			m.cycleCompletion(1)
		}
		return true
	case "down":
		m.cycleCompletion(1)
		return true
	case "shift+tab", "up":
		m.cycleCompletion(-1)
		return true
	case "enter":
		m.acceptCompletion()
		return true
	case "esc":
		m.engine.Dismiss(cs.res)
		m.completion = nil
		m.relayout()
		return true
	}
	return false
}

// cycleCompletion moves the highlight by delta with wrap, keeping it
// inside the visible window. In no-list mode there is no window to
// keep it inside; instead the very first call after opening only
// reveals the already-selected candidate (index 0), since nothing has
// been shown yet, and every call after that moves first, then writes —
// the same order a visible list's highlight already implies before any
// key is pressed. previewed only flips once writePreview actually
// writes something: a candidate refused for CharLimit (see
// writePreview) must not count as the reveal, or the very next key
// would advance past index 0 having shown the user nothing at all.
func (m *Model) cycleCompletion(delta int) {
	cs := m.completion
	n := len(cs.res.Candidates)
	if m.noList() {
		if cs.previewed {
			cs.index = (cs.index + delta + n) % n
		}
		if m.writePreview() {
			cs.previewed = true
		}
		return
	}
	cs.index = (cs.index + delta + n) % n
	m.clampWindow()
}

// spliceValue replaces value[start:end] (byte offsets) with text and
// writes the result back into the input widget.
func (m *Model) spliceValue(start, end int, text string) {
	v := m.input.Value()
	if start < 0 {
		start = 0
	}
	if end > len(v) {
		end = len(v)
	}
	m.input.SetValue(v[:start] + text + v[end:])
}

// spliceExceedsLimit reports whether replacing value[start:end] (byte
// offsets) with text would leave more runes than the input's CharLimit
// allows (CharLimit <= 0 means unlimited). textinput.SetValue enforces
// the limit itself, but silently, by truncating the *end* of the
// result — which throws away characters the user typed rather than the
// completion that caused the overflow, so callers must check first and
// refuse the splice instead.
func (m *Model) spliceExceedsLimit(start, end int, text string) bool {
	limit := m.input.CharLimit
	if limit <= 0 {
		return false
	}
	v := m.input.Value()
	if start < 0 {
		start = 0
	}
	if end > len(v) {
		end = len(v)
	}
	return utf8.RuneCountInString(v[:start])+utf8.RuneCountInString(text)+
		utf8.RuneCountInString(v[end:]) > limit
}

// writePreview is no-list mode's stand-in for a visible list: it writes
// the highlighted candidate's Insert, trailing space trimmed, over the
// span written so far, and moves the cursor to the end of it. Accept
// adds the space back. wrote reports whether it actually did: the
// CharLimit check is against the full Insert, space included, not the
// trimmed preview written here — a candidate that only fits without
// its trailing space could never be accepted either, so it must never
// be previewed as if it could be. On a refusal the line and the span
// are left exactly as they were, so the next key can still try a
// different candidate.
func (m *Model) writePreview() bool {
	cs := m.completion
	cand := cs.res.Candidates[cs.index]
	if m.spliceExceedsLimit(cs.res.Start, cs.end, cand.Insert) {
		return false
	}
	preview := strings.TrimSuffix(cand.Insert, " ")
	m.spliceValue(cs.res.Start, cs.end, preview)
	cs.end = cs.res.Start + len(preview)
	m.input.SetCursor(runeOffset(m.input.Value(), cs.end))
	return true
}

// acceptCompletion splices the highlighted candidate's full Insert
// (trailing space included) over the span, moves the cursor after it,
// closes the list and sends one typing notice — the notice ordinary
// typing would have sent for this keystroke, since accept changes the
// line. It does not recompute, so the freshly inserted "@alice " does
// not reopen the list on the same keystroke; the next keystroke
// recomputes as usual.
//
// If the splice would push the line past CharLimit, the line is left
// untouched (never silently truncated) and the list simply closes,
// without a typing notice — no line changed, so nothing was typed.
// It is not dismissed: a dismissal only clears once the span's start
// moves, and backspacing inside the word to make room for the
// candidate does not move it, so a dismiss here would leave the list
// unreachable until the user typed a whole new trigger. Closing
// without dismissing lets the very next keystroke recompute and reopen
// normally; a repeated accept on an unrecoverable line simply repeats
// the refusal.
func (m *Model) acceptCompletion() {
	cs := m.completion
	cand := cs.res.Candidates[cs.index]
	if m.spliceExceedsLimit(cs.res.Start, cs.end, cand.Insert) {
		m.completion = nil
		m.relayout()
		m.push(item{kind: "event", text: m.st.err.Render(fmt.Sprintf(
			"completion would exceed the %d-character limit", m.input.CharLimit))})
		return
	}
	m.spliceValue(cs.res.Start, cs.end, cand.Insert)
	end := cs.res.Start + len(cand.Insert)
	m.input.SetCursor(runeOffset(m.input.Value(), end))
	m.completion = nil
	m.relayout()
	if m.conn != nil {
		m.conn.SendTyping()
	}
}

// refresh rebuilds the viewport content from the transcript.
func (m *Model) refresh(follow bool) {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	var lines []string
	wrap := lipgloss.NewStyle().Width(m.vp.Width())
	for _, it := range m.items {
		lines = append(lines, wrap.Render(m.renderItem(it)))
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
	if follow && atBottom || !follow {
		m.vp.GotoBottom()
	}
}

// renderItem draws one transcript row.
func (m *Model) renderItem(it item) string {
	prefix := ""
	if m.showTimes {
		prefix = m.st.time.Render(it.at.Local().Format("15:04")) + " "
	}
	switch it.kind {
	case "gap":
		return prefix + m.st.event.Render("— reconnected, older messages missing —")
	case "event", "server":
		if it.text != "" {
			return prefix + it.text
		}
		return prefix + m.st.event.Render(render.Text(it.html))
	case "me", "slap":
		return prefix + m.st.event.Render("* "+render.Line(it.username)+" "+
			render.Text(it.html))
	}

	name := m.st.user
	if it.isMod {
		name = m.st.modUser
	}
	if it.username == m.self {
		name = m.st.self
	}
	if it.mention {
		name = m.st.mention
	}
	speaker := name.Render("<" + render.Line(it.username) + ">")
	if it.kind == "dm" {
		speaker = m.st.dm.Render("[DM] ") + speaker
	}
	body := render.Text(it.html)
	if it.spoiler && !m.revealSpoilers {
		body = m.st.spoiler.Render("▒▒▒ spoiler — ctrl+s to reveal ▒▒▒")
	}
	return prefix + speaker + " " + body
}

// View implements tea.Model. The alt screen is declared here rather
// than as a program option: that is how v2 owns terminal modes.
func (m Model) View() tea.View {
	v := tea.NewView(m.content())
	v.AltScreen = true
	return v
}

// content draws the whole screen: status bar, transcript, input, help.
func (m Model) content() string {
	if m.splash != nil && m.splash.fits {
		return m.splashView()
	}
	if !m.ready {
		return "connecting…"
	}
	status := fmt.Sprintf(" #%s · %s", m.channel, m.stateLabel())
	if n := len(m.users); n > 0 {
		status += fmt.Sprintf(" · %d %s", n, plural(n, "user", "users"))
	}
	if names := m.typingNames(); names != "" {
		status += " · " + names + " typing…"
	}
	// One line, always: a long stop reason would otherwise wrap the bar
	// and push the layout off the bottom of the screen.
	status = xansi.Truncate(status, max(0, m.vp.Width()-2), "…")
	// Truncated the same way: a narrow terminal would otherwise wrap
	// this into a second line and throw off every row count above it.
	helpText := xansi.Truncate(
		" enter send · tab complete · /who · pgup/pgdn scroll · "+
			"ctrl+s spoilers · ctrl+t times · ctrl+c quit",
		max(0, m.vp.Width()), "…")
	help := m.st.help.Render(helpText)
	rows := []string{
		m.st.status.Width(m.vp.Width()).Render(status),
		m.vp.View(),
	}
	if n := m.listRows(); n >= 1 {
		rows = append(rows, m.completionView(n))
	}
	rows = append(rows, m.input.View(), help)
	return strings.Join(rows, "\n")
}

// completionView draws the open completion list: a header row of key
// hints (plus a "+N more" tail when candidates run past the window),
// then n candidate rows from top. Every row is truncated to the
// viewport's width so the screen stays exactly `height` lines.
//
// This is read-only: top is always valid by the time this runs because
// every path that can change listRows or the candidate count —
// layout's resize, cycleCompletion's own moves, recompute opening or
// replacing the list — reclamps or resets top itself (clampWindow,
// or a fresh top: 0). View must never mutate model state through the
// completion pointer, so it does not clamp here too.
func (m Model) completionView(n int) string {
	cs := m.completion
	w := m.vp.Width()
	header := " tab/shift+tab next · enter accept · esc close"
	if more := len(cs.res.Candidates) - (cs.top + n); more > 0 {
		header += fmt.Sprintf(" · +%d more", more)
	}
	rows := make([]string, 0, n+1)
	rows = append(rows, xansi.Truncate(m.st.help.Render(header), w, "…"))
	for i := 0; i < n; i++ {
		idx := cs.top + i
		rows = append(rows, m.completionRow(cs.res.Candidates[idx], idx == cs.index, w))
	}
	return strings.Join(rows, "\n")
}

// completionRow draws one candidate: a leading space, the label (dim
// candidates in the away style), then the detail in the help style
// when present. Label and Detail come from the network in later
// phases, so both go through render.Line here rather than at the
// source. The highlighted row is wrapped in the pick style and padded
// to w after truncation, which is what keeps it reading as a full-width
// bar under NO_COLOR (Reverse survives where color does not).
func (m Model) completionRow(c complete.Candidate, active bool, w int) string {
	label := render.Line(c.Label)
	if c.Dim {
		label = m.st.away.Render(label)
	}
	row := " " + label
	if c.Detail != "" {
		row += "  " + m.st.help.Render(render.Line(c.Detail))
	}
	row = xansi.Truncate(row, w, "…")
	if active {
		// Stripped first: wrapping the Dim/Detail sub-styles as-is would
		// nest pick's Reverse inside their own resets, breaking the bar
		// into styled/unstyled/styled bands instead of one run. The
		// highlight itself is what needs to read under NO_COLOR (ADR
		// 0005), not the sub-styling underneath it.
		row = m.st.pick.Width(w).Render(xansi.Strip(row))
	}
	return row
}

// splashView centers the current frame with the connection state and
// the skip hint under it. The terminal keeps its own background (ADR
// 0005): the wordmark is drawn over it, not on a painted base surface.
func (m Model) splashView() string {
	s := m.splash
	caption := lipgloss.NewStyle().Width(s.art.W).Align(lipgloss.Center)
	// One line, like the status bar: a long retry reason would wrap past
	// the rows fitSplash reserved and push the hint off the screen. The
	// transcript row keeps the full reason.
	label := xansi.Truncate(m.stateLabel(), s.art.W, "…")
	block := strings.Join(s.effect.Frame(s.art, s.palette, s.elapsed), "\n") +
		"\n\n" + caption.Render(m.st.event.Render(label)) +
		"\n" + caption.Render(m.st.help.Render("any key to skip"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		block)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// whoText lists the roster on one wrapped row: sorted by name, mods
// marked with @, away users dimmed with their away message.
func (m Model) whoText() string {
	if len(m.users) == 0 {
		return m.st.event.Render("no user list yet")
	}
	users := make([]protocol.ChannelUser, 0, len(m.users))
	for _, u := range m.users {
		users = append(users, u)
	}
	sort.Slice(users, func(i, j int) bool {
		return strings.ToLower(users[i].Username) <
			strings.ToLower(users[j].Username)
	})
	names := make([]string, 0, len(users))
	for _, u := range users {
		name := render.Line(u.Username)
		if u.IsAdmin || u.IsChatMod {
			name = "@" + name
		}
		if u.IsAway {
			if msg := strings.TrimSpace(render.Text(u.AwayMessage)); msg != "" {
				name += " (away: " + msg + ")"
			} else {
				name += " (away)"
			}
			name = m.st.away.Render(name)
		}
		names = append(names, name)
	}
	n := len(users)
	return fmt.Sprintf("%d %s: %s", n, plural(n, "user", "users"),
		strings.Join(names, ", "))
}

// stateLabel names the connection state for the status bar.
func (m Model) stateLabel() string {
	switch m.state {
	case client.StateOnline:
		return "online"
	case client.StateReconnecting:
		if m.retryErr != nil {
			return "reconnecting… (" + render.Line(m.retryErr.Error()) + ")"
		}
		return "reconnecting…"
	case client.StateStopped:
		if m.stopErr != nil {
			return "disconnected: " + render.Line(m.stopErr.Error())
		}
		return "disconnected"
	default:
		return "connecting…"
	}
}

// typingNames lists who is currently composing.
func (m Model) typingNames() string {
	var names []string
	for name := range m.typing {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
