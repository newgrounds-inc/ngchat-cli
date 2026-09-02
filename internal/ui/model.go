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

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
	"github.com/newgrounds-inc/ngchat-cli/internal/render"
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

var (
	statusStyle = lipgloss.NewStyle().Reverse(true).Padding(0, 1)
	userStyle   = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("10"))
	modUserStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("11"))
	selfStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("14"))
	mentionStyle = lipgloss.NewStyle().Bold(true).Reverse(true).
			Foreground(lipgloss.Color("11"))
	dmStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	eventStyle   = lipgloss.NewStyle().Faint(true).Italic(true)
	spoilerStyle = lipgloss.NewStyle().Faint(true)
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	helpStyle    = lipgloss.NewStyle().Faint(true)
	timeStyle    = lipgloss.NewStyle().Faint(true)
	awayStyle    = lipgloss.NewStyle().Faint(true)
)

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
}

// New builds the UI over a running chat client.
func New(chat *client.Client, opts Options) Model {
	input := textinput.New()
	input.Placeholder = "message #" + opts.Channel
	input.CharLimit = maxMessageLength
	input.Focus()
	return Model{
		chat:    chat,
		channel: opts.Channel,
		quiet:   opts.Quiet,
		bell:    os.Stdout,
		input:   input,
		typing:  map[string]typingState{},
		users:   map[int]protocol.ChannelUser{},
	}
}

// tickMsg drives typing-indicator expiry.
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
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
	return tea.Batch(waitEvent(m.chat.Events()), tick(), textinput.Blink)
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.layout(msg.Width, msg.Height)
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			return m, tea.Quit
		case tea.KeyCtrlS:
			m.revealSpoilers = !m.revealSpoilers
			m.refresh(false)
			return m, nil
		case tea.KeyCtrlT:
			m.showTimes = !m.showTimes
			m.refresh(false)
			return m, nil
		case tea.KeyPgUp, tea.KeyPgDown:
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		case tea.KeyEnter:
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
					text: errorStyle.Render("send failed: " + err.Error())})
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
		if m.SignedOut() {
			// Nothing inside the TUI can fix a revoked remember cookie
			// (no inline re-login), so leave the screen and let main
			// print the one useful instruction.
			return m, tea.Quit
		}
		return m, waitEvent(m.chat.Events())
	}
	return m, nil
}

// SignedOut reports whether the client stopped because the site refused
// the remember cookie. main reads it after the program exits.
func (m Model) SignedOut() bool {
	return m.state == client.StateStopped &&
		errors.Is(m.stopErr, auth.ErrSignedOut)
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
	case client.StateReconnecting:
		m.retryErr = e.Err
	}
	if e.Gap {
		m.push(item{kind: "gap", at: time.Now()})
	}
	switch msg := e.Msg.(type) {
	case protocol.Authenticated:
		m.self = msg.Username
		if motd := msg.MOTDText(); motd != "" {
			m.pushEvent(eventStyle.Render(render.Text(motd)), msg.ServerTime)
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
		if msg.Username != "" && msg.Username != m.self {
			m.typing[msg.Username] = typingState{msg.Username, time.Now()}
		}
	case protocol.UserJoined:
		m.users[msg.UserID] = rosterEntry(msg)
		m.pushEvent(eventStyle.Render(msg.Username+" joined"), msg.ServerTime)
	case protocol.UserUpdated:
		// A silent roster patch: never a join notice (ADR 0002 drift note
		// on the upstream schema).
		m.users[msg.UserID] = rosterEntry(protocol.UserJoined(msg))
	case protocol.UserLeft:
		delete(m.users, msg.UserID)
		m.pushEvent(eventStyle.Render(msg.Username+" left"), msg.ServerTime)
		delete(m.typing, msg.Username)
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
		m.pushEvent(awayText(msg), msg.ServerTime)
	case protocol.Revalidated:
		// The socket renewed its token in place; the flags on this
		// message now describe self. Nothing in the transcript depends on
		// them yet, so the only visible effect is that no reconnect
		// divider appears.
	case client.RenewalFailed:
		m.pushEvent(eventStyle.Render(
			"token renewal failed ("+msg.Err.Error()+
				"); will reconnect when the current token expires"), 0)
	case protocol.Unauthorized:
		// Only reaches the UI after authentication, right before the
		// server closes the socket (expiry or a rejected renewal).
		m.pushEvent(eventStyle.Render(msg.Message), 0)
	case protocol.Error:
		m.pushEvent(errorStyle.Render(msg.Message), 0)
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
func awayText(a protocol.Away) string {
	if !a.IsAway {
		return eventStyle.Render(a.Username + " is back")
	}
	text := a.Username + " is away"
	if a.AwayMessage != "" {
		text += ": " + render.Text(a.AwayMessage)
	}
	return eventStyle.Render(text)
}

// pushNotices replays the newest rows of the away-inbox, oldest first so
// the transcript reads downward, as faint event rows.
func (m *Model) pushNotices(notes []protocol.Notification) {
	if len(notes) > maxNotices {
		notes = notes[:maxNotices]
	}
	for i := len(notes) - 1; i >= 0; i-- {
		n := notes[i]
		who := n.Username
		if who == "" {
			who = "someone"
		}
		label := "while you were away · " + who + ": "
		if n.MessageType == "directMessage" || n.MessageType == "modDirectMessage" {
			label = "while you were away · [DM] " + who + ": "
		}
		m.pushEvent(eventStyle.Render(label+render.Text(n.Message)), n.ServerTime)
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
	delete(m.typing, msg.Username)
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
		m.vp = viewport.New(w, vh)
		m.ready = true
	} else {
		m.vp.Width = w
		m.vp.Height = vh
	}
	m.input.Width = max(10, w-4)
	m.refresh(false)
}

// refresh rebuilds the viewport content from the transcript.
func (m *Model) refresh(follow bool) {
	if !m.ready {
		return
	}
	atBottom := m.vp.AtBottom()
	var lines []string
	wrap := lipgloss.NewStyle().Width(m.vp.Width)
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
		prefix = timeStyle.Render(it.at.Local().Format("15:04")) + " "
	}
	switch it.kind {
	case "gap":
		return prefix + eventStyle.Render("— reconnected, older messages missing —")
	case "event", "server":
		if it.text != "" {
			return prefix + it.text
		}
		return prefix + eventStyle.Render(render.Text(it.html))
	case "me", "slap":
		return prefix + eventStyle.Render("* "+it.username+" "+
			render.Text(it.html))
	}

	name := userStyle
	if it.isMod {
		name = modUserStyle
	}
	if it.username == m.self {
		name = selfStyle
	}
	if it.mention {
		name = mentionStyle
	}
	speaker := name.Render("<" + it.username + ">")
	if it.kind == "dm" {
		speaker = dmStyle.Render("[DM] ") + speaker
	}
	body := render.Text(it.html)
	if it.spoiler && !m.revealSpoilers {
		body = spoilerStyle.Render("▒▒▒ spoiler — ctrl+s to reveal ▒▒▒")
	}
	return prefix + speaker + " " + body
}

// View implements tea.Model.
func (m Model) View() string {
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
	help := helpStyle.Render(
		" enter send · /who · pgup/pgdn scroll · ctrl+s spoilers · " +
			"ctrl+t times · ctrl+c quit")
	return statusStyle.Width(m.vp.Width).Render(status) + "\n" +
		m.vp.View() + "\n" +
		m.input.View() + "\n" +
		help
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
		return eventStyle.Render("no user list yet")
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
		name := u.Username
		if u.IsAdmin || u.IsChatMod {
			name = "@" + name
		}
		if u.IsAway {
			if msg := strings.TrimSpace(render.Text(u.AwayMessage)); msg != "" {
				name += " (away: " + msg + ")"
			} else {
				name += " (away)"
			}
			name = awayStyle.Render(name)
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
			return "reconnecting… (" + m.retryErr.Error() + ")"
		}
		return "reconnecting…"
	case client.StateStopped:
		if m.stopErr != nil {
			return "disconnected: " + m.stopErr.Error()
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
