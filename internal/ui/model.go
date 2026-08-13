// Package ui is the fullscreen Bubble Tea interface: status bar, message
// viewport, input line. Scrollback lives in the viewport (not the
// terminal's), so paging keys and spoiler re-rendering are handled here.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
	"github.com/newgrounds-inc/ngchat-cli/internal/render"
)

const maxMessageLength = 5000 // server-side limit per chat message

var (
	statusStyle = lipgloss.NewStyle().Reverse(true).Padding(0, 1)
	userStyle   = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("10"))
	modUserStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("11"))
	selfStyle = lipgloss.NewStyle().Bold(true).
			Foreground(lipgloss.Color("14"))
	dmStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	eventStyle   = lipgloss.NewStyle().Faint(true).Italic(true)
	spoilerStyle = lipgloss.NewStyle().Faint(true)
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	helpStyle    = lipgloss.NewStyle().Faint(true)
)

// item is one rendered row of the transcript.
type item struct {
	kind     string // "chat", "me", "slap", "server", "dm", "event", "gap"
	username string
	isMod    bool
	html     string // server-rendered HTML, converted lazily on render
	spoiler  bool
	text     string // pre-rendered text for non-chat rows
}

// typingState remembers who is composing and when they last said so.
type typingState struct {
	username string
	at       time.Time
}

// Model is the Bubble Tea root model.
type Model struct {
	chat    *client.Client
	channel string

	items          []item
	revealSpoilers bool
	state          client.State
	stopErr        error
	self           string
	typing         map[string]typingState

	vp    viewport.Model
	input textinput.Model
	ready bool
}

// New builds the UI over a running chat client.
func New(chat *client.Client, channel string) Model {
	input := textinput.New()
	input.Placeholder = "message #" + channel
	input.CharLimit = maxMessageLength
	input.Focus()
	return Model{
		chat:    chat,
		channel: channel,
		input:   input,
		typing:  map[string]typingState{},
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
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyCtrlS:
			m.revealSpoilers = !m.revealSpoilers
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
		return m, waitEvent(m.chat.Events())
	}
	return m, nil
}

// handleEvent folds a chat-client event into the transcript.
func (m *Model) handleEvent(e client.Event) {
	m.state = e.State
	if e.Err != nil {
		m.stopErr = e.Err
	}
	if e.Gap {
		m.push(item{kind: "gap"})
	}
	switch msg := e.Msg.(type) {
	case protocol.Authenticated:
		m.self = msg.Username
		if motd := msg.MOTDText(); motd != "" {
			m.push(item{kind: "event",
				text: eventStyle.Render(render.Text(motd))})
		}
	case protocol.Message:
		m.pushMessage(msg)
	case protocol.TypingEvent:
		if msg.Username != "" && msg.Username != m.self {
			m.typing[msg.Username] = typingState{msg.Username, time.Now()}
		}
	case protocol.UserJoined:
		m.push(item{kind: "event",
			text: eventStyle.Render(msg.Username + " joined")})
	case protocol.UserLeft:
		m.push(item{kind: "event",
			text: eventStyle.Render(msg.Username + " left")})
		delete(m.typing, msg.Username)
	case protocol.Error:
		m.push(item{kind: "event",
			text: errorStyle.Render(msg.Message)})
	}
}

// pushMessage classifies a wire message into a transcript item.
func (m *Model) pushMessage(msg protocol.Message) {
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
	m.push(item{
		kind:     kind,
		username: msg.Username,
		isMod:    msg.IsAdmin || msg.IsChatMod,
		html:     msg.Message,
		spoiler:  msg.IsSpoiler,
	})
}

// push appends a transcript item and re-renders, following the bottom
// unless the user has scrolled up.
func (m *Model) push(it item) {
	m.items = append(m.items, it)
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
	switch it.kind {
	case "gap":
		return eventStyle.Render("— reconnected, older messages missing —")
	case "event", "server":
		if it.text != "" {
			return it.text
		}
		return eventStyle.Render(render.Text(it.html))
	case "me", "slap":
		return eventStyle.Render("* " + it.username + " " +
			render.Text(it.html))
	}

	name := userStyle
	if it.isMod {
		name = modUserStyle
	}
	if it.username == m.self {
		name = selfStyle
	}
	prefix := name.Render("<" + it.username + ">")
	if it.kind == "dm" {
		prefix = dmStyle.Render("[DM] ") + prefix
	}
	body := render.Text(it.html)
	if it.spoiler && !m.revealSpoilers {
		body = spoilerStyle.Render("▒▒▒ spoiler — ctrl+s to reveal ▒▒▒")
	}
	return prefix + " " + body
}

// View implements tea.Model.
func (m Model) View() string {
	if !m.ready {
		return "connecting…"
	}
	status := fmt.Sprintf(" #%s · %s", m.channel, m.stateLabel())
	if names := m.typingNames(); names != "" {
		status += " · " + names + " typing…"
	}
	help := helpStyle.Render(
		" enter send · pgup/pgdn scroll · ctrl+s spoilers · ctrl+c quit")
	return statusStyle.Width(m.vp.Width).Render(status) + "\n" +
		m.vp.View() + "\n" +
		m.input.View() + "\n" +
		help
}

// stateLabel names the connection state for the status bar.
func (m Model) stateLabel() string {
	switch m.state {
	case client.StateOnline:
		return "online"
	case client.StateReconnecting:
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
	return strings.Join(names, ", ")
}
