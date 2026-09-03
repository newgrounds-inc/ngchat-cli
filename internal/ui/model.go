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

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
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
	help, time, away lipgloss.Style
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
	channel string
	quiet   bool
	bell    io.Writer // where the BEL byte goes; the terminal in production

	items          []item
	revealSpoilers bool
	showTimes      bool
	state          client.State
	stopErr        error
	retryErr       error // why the last session ended, while reconnecting
	self           string
	motd           string // server HTML from Authenticated, shown after each backfill
	typing         map[string]typingState
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
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
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
				return m, nil
			}
			if err := m.chat.SendChat(text); err != nil {
				m.push(item{kind: "event",
					text: m.st.err.Render("send failed: " + err.Error())})
				return m, nil
			}
			m.input.Reset()
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != "" {
			m.chat.SendTyping()
		}
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
		// message now describe self. Nothing in the transcript depends on
		// them yet, so the only visible effect is that no reconnect
		// divider appears.
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

// layout applies terminal dimensions to the widgets.
func (m *Model) layout(w, h int) {
	inputHeight := 1
	statusHeight := 1
	helpHeight := 1
	vh := max(1, h-inputHeight-statusHeight-helpHeight)
	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(w), viewport.WithHeight(vh))
		m.ready = true
	} else {
		m.vp.SetWidth(w)
		m.vp.SetHeight(vh)
	}
	m.input.SetWidth(max(10, w-4))
	m.refresh(false)
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
	help := m.st.help.Render(
		" enter send · /who · pgup/pgdn scroll · ctrl+s spoilers · " +
			"ctrl+t times · ctrl+c quit")
	return m.st.status.Width(m.vp.Width()).Render(status) + "\n" +
		m.vp.View() + "\n" +
		m.input.View() + "\n" +
		help
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
