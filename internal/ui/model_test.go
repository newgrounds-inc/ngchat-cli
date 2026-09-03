package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/complete"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
	"github.com/newgrounds-inc/ngchat-cli/internal/splash"
	"github.com/newgrounds-inc/ngchat-cli/internal/theme"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;;[^\x1b]*\x1b\\`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// newModel builds a Model with no chat client; the tests here never send, and
// the transcript renders lazily so no viewport is needed.
func newModel() Model { return New(nil, Options{Channel: "general"}) }

func TestPushMessageClassifiesVariants(t *testing.T) {
	tests := []struct {
		wireName string
		wantKind string
	}{
		{"message", "chat"},
		{"meMessage", "me"},
		{"slapMessage", "slap"},
		{"serverMessage", "server"},
		{"directMessage", "dm"},
		{"somethingNew", "chat"}, // unrecognized variants still display
	}

	for _, tc := range tests {
		t.Run(tc.wireName, func(t *testing.T) {
			m := newModel()
			m.pushMessage(protocol.Message{
				Name: tc.wireName, Username: "bob", Message: "hi",
			}, false)
			if len(m.items) != 1 {
				t.Fatalf("got %d items, want 1", len(m.items))
			}
			if m.items[0].kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", m.items[0].kind, tc.wantKind)
			}
		})
	}
}

// TestSpoilerToggle: the raw HTML is kept on the item so ctrl+s can re-render
// from source instead of losing the hidden text.
func TestSpoilerToggle(t *testing.T) {
	m := newModel()
	m.pushMessage(protocol.Message{
		Name: "message", Username: "bob",
		Message: "the butler did it", IsSpoiler: true,
	}, false)

	hidden := plain(m.renderItem(m.items[0]))
	if strings.Contains(hidden, "butler") {
		t.Errorf("spoiler text leaked while hidden: %q", hidden)
	}
	if !strings.Contains(hidden, "ctrl+s") {
		t.Errorf("hidden spoiler should say how to reveal it: %q", hidden)
	}

	m.revealSpoilers = true
	revealed := plain(m.renderItem(m.items[0]))
	if !strings.Contains(revealed, "the butler did it") {
		t.Errorf("revealed spoiler = %q", revealed)
	}
}

func TestRenderItemFormatsSpeaker(t *testing.T) {
	m := newModel()
	m.self = "me"

	m.pushMessage(protocol.Message{Name: "message", Username: "bob",
		Message: "hi"}, false)
	if got := plain(m.renderItem(m.items[0])); got != "<bob> hi" {
		t.Errorf("chat row = %q", got)
	}

	m.pushMessage(protocol.Message{Name: "directMessage", Username: "bob",
		Message: "psst"}, false)
	if got := plain(m.renderItem(m.items[1])); !strings.HasPrefix(got, "[DM] ") {
		t.Errorf("dm row = %q, want a [DM] prefix", got)
	}

	m.pushMessage(protocol.Message{Name: "meMessage", Username: "bob",
		Message: "waves"}, false)
	if got := plain(m.renderItem(m.items[2])); got != "* bob waves" {
		t.Errorf("me row = %q", got)
	}
}

// TestRenderItemConvertsHTML confirms the transcript goes through the render
// package rather than printing raw markup.
func TestRenderItemConvertsHTML(t *testing.T) {
	m := newModel()
	m.pushMessage(protocol.Message{Name: "message", Username: "bob",
		Message: `say <strong>hi</strong> to <a href="https://x.test">x</a>`}, false)

	got := plain(m.renderItem(m.items[0]))
	if strings.Contains(got, "<strong>") {
		t.Errorf("raw HTML reached the transcript: %q", got)
	}
	if !strings.Contains(got, "say hi to x") {
		t.Errorf("row = %q", got)
	}
}

func TestHandleEventGapMarker(t *testing.T) {
	m := newModel()
	m.handleEvent(client.Event{State: client.StateOnline, Gap: true})

	if len(m.items) != 1 || m.items[0].kind != "gap" {
		t.Fatalf("expected a gap marker, got %+v", m.items)
	}
	if got := plain(m.renderItem(m.items[0])); !strings.Contains(got, "missing") {
		t.Errorf("gap row should say history is missing: %q", got)
	}
}

func TestHandleEventAuthenticatedSetsSelf(t *testing.T) {
	m := newModel()
	m.handleEvent(client.Event{Msg: protocol.Authenticated{Username: "bob"}})

	if m.self != "bob" {
		t.Errorf("self = %q, want bob", m.self)
	}
	m.handleEvent(client.Event{Msg: client.BackfillDone{}})
	if len(m.items) != 0 {
		t.Errorf("an empty MOTD should not push a row: %+v", m.items)
	}
}

// TestMOTDFollowsBackfill: the banner lands under the replayed history,
// not above it where the backfill would scroll it away.
func TestMOTDFollowsBackfill(t *testing.T) {
	m := newModel()
	m.handleEvent(client.Event{Msg: protocol.Authenticated{Username: "bob",
		MOTD: json.RawMessage(`"Welcome to <b>NG Chat</b>"`)}})
	if len(m.items) != 0 {
		t.Fatalf("MOTD pushed before the backfill: %+v", m.items)
	}
	m.handleEvent(client.Event{Backfill: true, Msg: protocol.Message{
		Name: "message", Username: "ann", Message: "old"}})
	m.handleEvent(client.Event{Msg: client.BackfillDone{}})
	if len(m.items) != 2 {
		t.Fatalf("rows = %d, want history then MOTD", len(m.items))
	}
	if got := plain(m.renderItem(m.items[1])); got != "Welcome to NG Chat" {
		t.Errorf("MOTD row = %q", got)
	}
}

// TestTypingIgnoresSelf: the server echoes our own typing events back, and
// showing "you are typing" in the status bar would be noise.
func TestTypingIgnoresSelf(t *testing.T) {
	m := newModel()
	m.self = "me"

	m.handleEvent(client.Event{Msg: protocol.TypingEvent{Username: "me"}})
	m.handleEvent(client.Event{Msg: protocol.TypingEvent{Username: ""}})
	if len(m.typing) != 0 {
		t.Errorf("typing = %v, want empty", m.typing)
	}

	m.handleEvent(client.Event{Msg: protocol.TypingEvent{Username: "bob"}})
	if m.typingNames() != "bob" {
		t.Errorf("typingNames() = %q, want bob", m.typingNames())
	}
}

// TestTypingClearedByMessage stops the indicator the moment the line lands.
func TestTypingClearedByMessage(t *testing.T) {
	m := newModel()
	m.handleEvent(client.Event{Msg: protocol.TypingEvent{Username: "bob"}})
	m.handleEvent(client.Event{Msg: protocol.Message{
		Name: "message", Username: "bob", Message: "hi"}})

	if len(m.typing) != 0 {
		t.Errorf("typing should clear once the message arrives: %v", m.typing)
	}
}

func TestTypingClearedOnLeave(t *testing.T) {
	m := newModel()
	m.handleEvent(client.Event{Msg: protocol.TypingEvent{Username: "bob"}})
	m.handleEvent(client.Event{Msg: protocol.UserLeft{Username: "bob"}})

	if len(m.typing) != 0 {
		t.Errorf("typing should clear when the user leaves: %v", m.typing)
	}
	if len(m.items) != 1 || m.items[0].kind != "event" {
		t.Errorf("expected a leave notice, got %+v", m.items)
	}
}

func TestStateLabel(t *testing.T) {
	tests := []struct {
		name  string
		event client.Event
		want  string
	}{
		{"connecting", client.Event{State: client.StateConnecting},
			"connecting…"},
		{"online", client.Event{State: client.StateOnline}, "online"},
		{"reconnecting", client.Event{State: client.StateReconnecting},
			"reconnecting…"},
		{"stopped", client.Event{State: client.StateStopped}, "disconnected"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel()
			m.handleEvent(tc.event)
			if got := m.stateLabel(); got != tc.want {
				t.Errorf("stateLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTypingExpiry mirrors the one-second tick that ages out stale
// indicators, without waiting four seconds for it.
func TestTypingExpiry(t *testing.T) {
	m := newModel()
	m.typing["bob"] = typingState{"bob", time.Now().Add(-5 * time.Second)}
	m.typing["ann"] = typingState{"ann", time.Now()}

	if _, cmd := m.Update(tickMsg(time.Now())); cmd == nil {
		t.Error("tick should reschedule itself")
	}
	if _, stale := m.typing["bob"]; stale {
		t.Error("a 5s-old typing indicator should have expired")
	}
	if _, fresh := m.typing["ann"]; !fresh {
		t.Error("a fresh typing indicator should survive")
	}
}

// TestSignedOutQuits: a stop caused by auth.ErrSignedOut ends the program
// rather than sitting on a "disconnected" status bar the user cannot act
// on from inside the TUI.
func TestSignedOutQuits(t *testing.T) {
	m := newModel()
	next, cmd := m.Update(client.Event{State: client.StateStopped,
		Err: fmt.Errorf("signed out: %w", auth.ErrSignedOut)})
	if !next.(Model).SignedOut() {
		t.Fatal("SignedOut() = false")
	}
	if cmd == nil {
		t.Fatal("no command returned, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("command produced %T, want tea.QuitMsg", cmd())
	}

	// Any other stop keeps the screen up with the reason visible, in
	// the status bar and as a transcript row.
	m = newModel()
	m.handleEvent(client.Event{State: client.StateStopped,
		Err: errors.New("kicked")})
	if m.SignedOut() || m.leaves() {
		t.Error("an unrelated stop must not leave the TUI")
	}
	if plain(m.stateLabel()) != "disconnected: kicked" {
		t.Errorf("status = %q", m.stateLabel())
	}
	if len(m.items) != 1 || plain(m.renderItem(m.items[0])) != "disconnected: kicked" {
		t.Errorf("transcript rows = %+v, want the stop reason", m.items)
	}
}

// TestAccessDeniedQuitsWithNotice: the server's entry-gate HTML is
// rendered for main to print, and the TUI leaves like a signed-out run.
func TestAccessDeniedQuitsWithNotice(t *testing.T) {
	m := newModel()
	next, cmd := m.Update(client.Event{State: client.StateStopped,
		Err: fmt.Errorf("access denied: %w", &client.AccessDenied{
			Message: `NG Chat is a <a href="https://www.newgrounds.com/supporter">supporter only</a> feature, sorry 😕.`})})
	got := plain(next.(Model).Denied())
	want := "NG Chat is a supporter only <https://www.newgrounds.com/supporter> feature, sorry 😕."
	if got != want {
		t.Errorf("Denied() = %q, want %q", got, want)
	}
	if cmd == nil {
		t.Fatal("no command returned, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("command produced %T, want tea.QuitMsg", cmd())
	}
}

// TestStatusBarStaysOneLine: a long reason must truncate, never wrap the
// bar into the viewport's rows.
func TestStatusBarStaysOneLine(t *testing.T) {
	m := newModel()
	m.layout(40, 8)
	m.handleEvent(client.Event{State: client.StateReconnecting,
		Err: errors.New(strings.Repeat("long reason ", 20))})
	first := strings.SplitN(m.View().Content, "\n", 2)[0]
	if lipgloss.Width(first) > 40 {
		t.Errorf("status bar is %d cells wide, want ≤ 40", lipgloss.Width(first))
	}
	if lines := strings.Count(m.View().Content, "\n"); lines != 8-1 {
		t.Errorf("view has %d lines, want exactly the terminal height", lines+1)
	}
}

func joined(id int, name string, mod bool) client.Event {
	return client.Event{Msg: protocol.UserJoined{UserID: id, Username: name,
		IsChatMod: mod, ServerTime: 1}}
}

// TestRosterFollowsPresence: Subscribed seeds the list, joins and leaves
// patch it, userUpdated patches silently, and away flags the entry.
func TestRosterFollowsPresence(t *testing.T) {
	m := newModel()
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "ann"}, {UserID: 2, Username: "bob", IsChatMod: true},
	}}})
	if len(m.users) != 2 {
		t.Fatalf("users after subscribe = %d, want 2", len(m.users))
	}
	m.handleEvent(joined(3, "carol", false))
	m.handleEvent(client.Event{Msg: protocol.UserLeft{UserID: 1, Username: "ann"}})
	rows := len(m.items)
	m.handleEvent(client.Event{Msg: protocol.UserUpdated{UserID: 3, Username: "carol",
		IsChatMod: true}})
	if len(m.items) != rows {
		t.Errorf("userUpdated pushed a row; it must be silent")
	}
	if !m.users[3].IsChatMod {
		t.Error("userUpdated did not patch the roster")
	}
	m.handleEvent(client.Event{Msg: protocol.Away{UserID: 2, Username: "bob",
		IsAway: true, AwayMessage: "<i>lunch</i>", ServerTime: 2}})
	if !m.users[2].IsAway || m.users[2].AwayMessage != "<i>lunch</i>" {
		t.Errorf("away did not patch bob: %+v", m.users[2])
	}
	last := plain(m.renderItem(m.items[len(m.items)-1]))
	if last != "bob is away: lunch" {
		t.Errorf("away row = %q", last)
	}
	m.handleEvent(client.Event{Msg: protocol.Away{UserID: 2, Username: "bob"}})
	if m.users[2].IsAway {
		t.Error("coming back did not clear the away flag")
	}
	if last := plain(m.renderItem(m.items[len(m.items)-1])); last != "bob is back" {
		t.Errorf("back row = %q", last)
	}
	// A fresh subscribe (reconnect) replaces the roster wholesale.
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 9, Username: "zed"}}}})
	if len(m.users) != 1 || m.users[9].Username != "zed" {
		t.Errorf("reconnect roster = %+v", m.users)
	}
}

func TestWhoListsSortedWithMarks(t *testing.T) {
	m := newModel()
	if got := plain(m.whoText()); got != "no user list yet" {
		t.Errorf("empty who = %q", got)
	}
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "Zed"},
		{UserID: 2, Username: "ann", IsChatMod: true},
		{UserID: 3, Username: "bob", IsAway: true, AwayMessage: "<b>brb</b>"},
		{UserID: 4, Username: "cy", IsAway: true},
	}}})
	want := "4 users: @ann, bob (away: brb), cy (away), Zed"
	if got := plain(m.whoText()); got != want {
		t.Errorf("who = %q, want %q", got, want)
	}
	if !isWho("/WHO") || isWho("/whois bob") || isWho("who") {
		t.Error("isWho should match exactly /who, case-insensitively")
	}
}

func TestStatusBarCountsUsers(t *testing.T) {
	m := newModel()
	m.layout(80, 24)
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{{UserID: 1, Username: "ann"}}}})
	if v := plain(m.View().Content); !strings.Contains(v, "1 user") || strings.Contains(v, "1 users") {
		t.Errorf("view = %q, want a singular user count", v)
	}
	m.handleEvent(joined(2, "bob", false))
	if v := plain(m.View().Content); !strings.Contains(v, "2 users") {
		t.Errorf("view = %q, want 2 users", v)
	}
}

// TestMentionRingsBell: a live mention or DM to self rings once and is
// highlighted; a backfilled one is highlighted but silent, self never
// counts, and -quiet silences everything.
func TestMentionRingsBell(t *testing.T) {
	m := newModel()
	m.self = "Me"
	var bell bytes.Buffer
	m.bell = &bell

	// The wire shape: lowercased, "@" kept (server url-processor.ts).
	m.handleEvent(client.Event{Msg: protocol.Message{Name: "message",
		Username: "bob", Message: "hi", Mentions: []string{"@me"}}})
	if bell.String() != "\a" {
		t.Errorf("bell = %q, want one BEL for a mention", bell.String())
	}
	if !m.items[0].mention {
		t.Error("mention row not flagged")
	}
	bell.Reset()
	m.handleEvent(client.Event{Msg: protocol.Message{Name: "directMessage",
		Username: "bob", Message: "psst"}})
	if bell.String() != "\a" {
		t.Errorf("bell = %q, want one BEL for a DM", bell.String())
	}
	bell.Reset()
	m.handleEvent(client.Event{Backfill: true, Msg: protocol.Message{Name: "message",
		Username: "bob", Message: "old", Mentions: []string{"@me"}}})
	if bell.Len() != 0 {
		t.Error("a backfilled mention must not ring")
	}
	if !m.items[2].mention {
		t.Error("a backfilled mention should still be highlighted")
	}
	m.handleEvent(client.Event{Msg: protocol.Message{Name: "directMessage",
		Username: "Me", Message: "echo of my own dm"}})
	m.handleEvent(client.Event{Msg: protocol.Message{Name: "message",
		Username: "bob", Message: "unrelated", Mentions: []string{"@!me", "@meh"}}})
	if bell.Len() != 0 || m.items[3].mention || m.items[4].mention {
		t.Error("self echo, group mentions and other names must neither ring nor highlight")
	}

	m.quiet = true
	m.handleEvent(client.Event{Msg: protocol.Message{Name: "directMessage",
		Username: "bob", Message: "psst"}})
	if bell.Len() != 0 {
		t.Error("-quiet must silence the bell")
	}
	if !m.items[5].mention {
		t.Error("-quiet must keep the highlight")
	}
}

func TestTimestampToggle(t *testing.T) {
	m := newModel()
	at := time.Date(2026, 9, 2, 13, 5, 0, 0, time.Local)
	m.handleEvent(client.Event{Msg: protocol.Message{Name: "message",
		Username: "bob", Message: "hi", ServerTime: at.UnixMilli()}})
	if got := plain(m.renderItem(m.items[0])); got != "<bob> hi" {
		t.Errorf("row without times = %q", got)
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	m = next.(Model)
	if got := plain(m.renderItem(m.items[0])); got != "13:05 <bob> hi" {
		t.Errorf("row with times = %q", got)
	}
}

// TestEscDoesNotQuit: esc used to end the program, which is far too easy
// to hit by reflex from a modal editor.
func TestEscDoesNotQuit(t *testing.T) {
	m := newModel()
	if _, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape}); cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Error("esc produced tea.Quit")
		}
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c should still quit")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Error("ctrl+c did not produce tea.Quit")
	}
}

func TestTranscriptCap(t *testing.T) {
	m := newModel()
	for i := 0; i < maxItems+50; i++ {
		m.push(item{kind: "event", text: fmt.Sprint(i)})
	}
	if len(m.items) != maxItems {
		t.Fatalf("items = %d, want %d", len(m.items), maxItems)
	}
	if m.items[0].text != "50" {
		t.Errorf("oldest kept = %q, want the 51st pushed", m.items[0].text)
	}
}

// TestNoticesShownOncePerRun: the server resends its whole away-inbox on
// every subscribe, so only the first one replays it, newest maxNotices
// rows, oldest first.
func TestNoticesShownOncePerRun(t *testing.T) {
	m := newModel()
	var notes []protocol.Notification
	for i := 0; i < maxNotices+5; i++ {
		notes = append(notes, protocol.Notification{
			Username: "ann", MessageType: "message",
			Message: fmt.Sprintf("n%d", i), ServerTime: int64(1000 - i)})
	}
	notes[0].MessageType = "modDirectMessage"
	m.handleEvent(client.Event{Msg: protocol.Subscribed{Notifications: notes}})
	if len(m.items) != maxNotices {
		t.Fatalf("rows = %d, want %d", len(m.items), maxNotices)
	}
	first := plain(m.renderItem(m.items[0]))
	last := plain(m.renderItem(m.items[maxNotices-1]))
	if first != "while you were away · ann: n9" {
		t.Errorf("first row = %q, want the oldest of the newest ten", first)
	}
	if last != "while you were away · [DM] ann: n0" {
		t.Errorf("last row = %q", last)
	}
	m.handleEvent(client.Event{Msg: protocol.Subscribed{Notifications: notes}})
	if len(m.items) != maxNotices {
		t.Errorf("a reconnect replayed the inbox again: %d rows", len(m.items))
	}
}

// TestNetworkTextCannotDriveTheTerminal: usernames, close reasons and
// server messages bypass the HTML renderer, so they get the same
// control stripping on their own way to the screen.
func TestNetworkTextCannotDriveTheTerminal(t *testing.T) {
	const evil = "bob\x1b[2J\x1b]52;c;xxx\x07\nmallory"
	m := newModel()
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.UserJoined{UserID: 1, Username: evil}})
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.Message{Name: "message", ID: ptr(int64(1)),
			Username: evil, Message: "hi"}})
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.Message{Name: "meMessage", ID: ptr(int64(2)),
			Username: evil, Message: "waves"}})
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.Message{Name: "slapMessage", ID: ptr(int64(3)),
			Username: evil, Message: "slaps"}})
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.Error{Message: evil}})
	m.handleEvent(client.Event{State: client.StateOnline,
		Msg: protocol.Away{UserID: 1, Username: evil, IsAway: true}})
	if len(m.items) != 6 {
		t.Fatalf("got %d rows, want 6", len(m.items))
	}
	for _, it := range m.items {
		got := m.renderItem(it)
		if p := plain(got); strings.ContainsAny(p, "\x1b\x07\n") {
			t.Errorf("%s row %q carries a control or a forged line", it.kind, got)
		}
	}
	m.handleEvent(client.Event{State: client.StateStopped,
		Err: errors.New(evil)})
	if got := plain(m.stateLabel()); strings.ContainsAny(got, "\x1b\x07\n") {
		t.Errorf("status %q carries a control", got)
	}
	if got := plain(m.whoText()); strings.ContainsAny(got, "\x1b\x07\n") {
		t.Errorf("who %q carries a control", got)
	}
}

func ptr[T any](v T) *T { return &v }

func splashModel() Model {
	return New(nil, Options{Channel: "general", Splash: splash.DefaultLaser})
}

// frameAt drives the splash clock to d after its start.
func frameAt(m Model, d time.Duration) (Model, tea.Cmd) {
	next, cmd := m.Update(frameMsg(m.splash.start.Add(d)))
	return next.(Model), cmd
}

func sized(m Model, w, h int) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(Model)
}

func TestNoSplashByDefault(t *testing.T) {
	if newModel().splash != nil {
		t.Fatal("a nil Splash option still built a splash")
	}
}

// TestSplashPlaysUntilOnline: the finished animation holds for the
// client, then hands over to the chat screen and stops scheduling
// frames.
func TestSplashPlaysUntilOnline(t *testing.T) {
	m := sized(splashModel(), 80, 24)
	if m.splash == nil || !m.splash.fits {
		t.Fatal("splash did not fit an 80×24 terminal")
	}
	v := m.View().Content
	if n := strings.Count(v, "\n"); n != 24-1 {
		t.Errorf("splash view has %d lines, want the terminal height", n+1)
	}
	for _, want := range []string{"any key to skip", "connecting…", "█"} {
		if !strings.Contains(plain(v), want) {
			t.Errorf("splash view lacks %q", want)
		}
	}
	if strings.Contains(plain(v), "#general") {
		t.Error("chat status bar shows during the splash")
	}

	m, cmd := frameAt(m, 100*time.Millisecond)
	if m.splash == nil || cmd == nil {
		t.Fatal("splash ended early or stopped scheduling frames")
	}
	m, _ = frameAt(m, splash.DefaultLaser.Duration())
	if m.splash == nil {
		t.Fatal("splash ended before the client was online")
	}
	m.handleEvent(client.Event{State: client.StateOnline})
	m, cmd = frameAt(m, splash.DefaultLaser.Duration()+frameRate)
	if m.splash != nil {
		t.Fatal("splash still up with the animation done and the client online")
	}
	if cmd != nil {
		t.Error("a frame was scheduled after the splash ended")
	}
	if !strings.Contains(plain(m.View().Content), "#general") {
		t.Error("chat screen did not take over after the splash")
	}
}

func TestSplashHoldsAtMostMaxSplash(t *testing.T) {
	m := sized(splashModel(), 80, 24)
	m, _ = frameAt(m, maxSplash-frameRate)
	if m.splash == nil {
		t.Fatal("splash gave up before maxSplash while still connecting")
	}
	m, _ = frameAt(m, maxSplash)
	if m.splash != nil {
		t.Fatal("splash held past maxSplash while still connecting")
	}
}

// TestSplashSkipsOnKey: a key ends the splash and is spent doing so;
// ctrl+c still quits outright.
func TestSplashSkipsOnKey(t *testing.T) {
	m := sized(splashModel(), 80, 24)
	next, _ := m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	m = next.(Model)
	if m.splash != nil {
		t.Fatal("a key did not skip the splash")
	}
	if m.input.Value() != "" {
		t.Errorf("the skip key reached the input: %q", m.input.Value())
	}

	m = sized(splashModel(), 80, 24)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+c during the splash produced no command")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Error("ctrl+c during the splash did not quit")
	}
}

// TestSplashSkippedWhenTooSmall: below the wordmark's scale-1 size the
// chat screen shows at once rather than a clipped logo.
func TestSplashSkippedWhenTooSmall(t *testing.T) {
	m := sized(splashModel(), 30, 10)
	if m.splash != nil {
		t.Fatal("splash kept on a 30×10 terminal")
	}
	if !strings.Contains(plain(m.View().Content), "#general") {
		t.Error("chat screen not shown after the splash was skipped")
	}
}

// TestStylesFollowTheme: the status bar carries the theme's primary
// color, which is the whole point of the theme seam. Classic's orange
// is distinct from the default's, so a default leaking through fails.
func TestStylesFollowTheme(t *testing.T) {
	classic, _ := theme.Lookup("classic")
	m := New(nil, Options{Channel: "general", Theme: classic})
	m.layout(40, 8)
	first := strings.SplitN(m.View().Content, "\n", 2)[0]
	if !strings.Contains(first, "38;2;235;117;34") {
		t.Errorf("status bar does not carry classic primary #eb7522: %q", first)
	}
	m.pushMessage(protocol.Message{Username: "bob", Message: "hi"}, false)
	row := m.renderItem(m.items[0])
	if !strings.Contains(row, "38;2;238;178;17") {
		t.Errorf("username does not carry classic username #eeb211: %q", row)
	}
}

// TestSplashCaptionStaysOneLine: a long retry reason under the wordmark
// must truncate, never wrap the block taller than the terminal.
func TestSplashCaptionStaysOneLine(t *testing.T) {
	m := sized(splashModel(), 40, 8)
	if m.splash == nil {
		t.Fatal("splash did not fit a 40×8 terminal")
	}
	m.handleEvent(client.Event{State: client.StateReconnecting,
		Err: errors.New(strings.Repeat("no such host ", 10))})
	v := m.View().Content
	if n := strings.Count(v, "\n"); n != 8-1 {
		t.Errorf("splash view has %d lines, want exactly the terminal height", n+1)
	}
	if !strings.Contains(plain(v), "any key to skip") {
		t.Error("the skip hint was pushed off the screen")
	}
}

// completionTrigger is the same @ mention regex complete's own stub in
// internal/complete/complete_test.go ports from upstream's
// regexes.test.ts; it is redefined here rather than exported because
// production sources do not exist until phase 1.
var completionTrigger = regexp.MustCompile(`\B@([a-zA-Z0-9-!*]*)$`)

var stubNames = []string{"alice", "alicia", "bob", "carol", "dave", "erin", "frank"}

// stubMentionSource is the fixed-list @ source the UI tests inject in
// place of the roster phase 1 will wire in.
type stubMentionSource struct{ names []string }

func (stubMentionSource) Name() string { return "stub" }

func (stubMentionSource) Match(line string, cursor int) (int, string, bool) {
	if cursor < 0 {
		cursor = 0
	} else if cursor > len(line) {
		cursor = len(line)
	}
	head := line[:cursor]
	loc := completionTrigger.FindStringSubmatchIndex(head)
	if loc == nil {
		return 0, "", false
	}
	return loc[0], head[loc[2]:loc[3]], true
}

func (s stubMentionSource) Candidates(term string) []complete.Candidate {
	names := complete.Rank(term, s.names, func(n string) string { return n })
	out := make([]complete.Candidate, len(names))
	for i, n := range names {
		out[i] = complete.Candidate{Insert: "@" + n + " ", Label: n}
	}
	return out
}

// fakeConn records what the completion tests send, standing in for a
// real *client.Client without a socket.
type fakeConn struct {
	typingNotices int
	sent          []string
	err           error
}

func (f *fakeConn) SendChat(text string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakeConn) SendTyping() { f.typingNotices++ }

// completionModel builds a model wired with the stub mention source and
// a fakeConn in place of the network, so completion tests can drive
// Update without a chat client.
func completionModel() (Model, *fakeConn) {
	m := newModel()
	m.sources = []complete.Source{stubMentionSource{names: stubNames}}
	conn := &fakeConn{}
	m.conn = conn
	return m, conn
}

// press feeds one key through Update and returns the resulting model,
// so a test reads as a sequence of key presses.
func press(m Model, msg tea.KeyPressMsg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

// typeText feeds each rune of s through Update as its own keypress, the
// way a real terminal delivers typing one character at a time.
func typeText(m Model, s string) Model {
	for _, r := range s {
		m = press(m, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func keyTab(m Model) Model { return press(m, tea.KeyPressMsg{Code: tea.KeyTab}) }
func keyShiftTab(m Model) Model {
	return press(m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
}
func keyUp(m Model) Model     { return press(m, tea.KeyPressMsg{Code: tea.KeyUp}) }
func keyDown(m Model) Model   { return press(m, tea.KeyPressMsg{Code: tea.KeyDown}) }
func keyEnterC(m Model) Model { return press(m, tea.KeyPressMsg{Code: tea.KeyEnter}) }
func keyEsc(m Model) Model    { return press(m, tea.KeyPressMsg{Code: tea.KeyEscape}) }

// TestCompletionOpensCyclesAndAccepts covers the everyday path: typing
// a trigger opens the list with the first candidate highlighted, tab
// moves the highlight, enter splices the highlighted candidate in and
// closes, and a term narrow enough for one candidate accepts on tab
// alone.
func TestCompletionOpensCyclesAndAccepts(t *testing.T) {
	m, conn := completionModel()
	m = sized(m, 80, 24)

	// "ali" (unlike "al") matches only alice and alicia by prefix; "al"
	// alone also picks up carol as a subsequence match.
	m = typeText(m, "@ali")
	if m.completion == nil || len(m.completion.res.Candidates) != 2 {
		t.Fatalf("completion after @ali = %+v, want 2 candidates", m.completion)
	}
	if m.completion.index != 0 || m.completion.res.Candidates[0].Label != "alice" {
		t.Fatalf("default highlight = %+v, want index 0 = alice", m.completion)
	}

	m = keyTab(m)
	if m.completion.index != 1 {
		t.Fatalf("tab did not move the highlight: index = %d", m.completion.index)
	}
	m = keyShiftTab(m) // back to @alice before accepting
	if m.completion.index != 0 {
		t.Fatalf("shift+tab did not move the highlight back: index = %d", m.completion.index)
	}

	conn.typingNotices = 0 // ignore the notices typing "@ali" itself sent
	m = keyEnterC(m)
	if m.completion != nil {
		t.Error("enter should close the completion")
	}
	if got, want := m.input.Value(), "@alice "; got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	if got, want := m.input.Position(), len([]rune("@alice ")); got != want {
		t.Errorf("cursor position = %d, want %d (end of the inserted text)", got, want)
	}
	if conn.typingNotices != 1 {
		t.Errorf("typing notices after accept = %d, want 1", conn.typingNotices)
	}

	// "alice" alone (unlike "al") narrows to a single candidate, so tab
	// accepts outright instead of cycling.
	m = typeText(m, "@alice")
	if len(m.completion.res.Candidates) != 1 {
		t.Fatalf("candidates for @alice = %d, want 1", len(m.completion.res.Candidates))
	}
	m = keyTab(m)
	if m.completion != nil {
		t.Error("tab on a single candidate should accept outright")
	}
	if !strings.HasSuffix(m.input.Value(), "@alice ") {
		t.Errorf("value = %q, want it to end with %q", m.input.Value(), "@alice ")
	}
}

// TestCompletionNavigationWrapsAndHasArrowAliases: shift+tab from the
// first candidate wraps to the last, and the arrows behave exactly
// like tab/shift+tab.
func TestCompletionNavigationWrapsAndHasArrowAliases(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 24)
	m = typeText(m, "@ali") // alice, alicia only

	if m.completion.index != 0 {
		t.Fatalf("initial index = %d, want 0", m.completion.index)
	}
	m = keyShiftTab(m)
	if m.completion.index != 1 {
		t.Errorf("shift+tab from 0 = %d, want wrap to 1", m.completion.index)
	}
	m = keyDown(m)
	if m.completion.index != 0 {
		t.Errorf("down from the last = %d, want wrap to 0", m.completion.index)
	}
	m = keyUp(m)
	if m.completion.index != 1 {
		t.Errorf("up from 0 = %d, want wrap to the last (1)", m.completion.index)
	}
}

// TestCompletionEscDismissesUntilSpanMoves: esc closes the list, more
// typing in the same span stays quiet, and a new trigger elsewhere
// reopens.
func TestCompletionEscDismissesUntilSpanMoves(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 24)
	m = typeText(m, "@al")

	m = keyEsc(m)
	if m.completion != nil {
		t.Fatal("esc did not close the completion")
	}
	m = typeText(m, "i") // "@al" -> "@ali", same span
	if m.completion != nil {
		t.Error("a dismissed span reopened after more typing inside it")
	}
	m = typeText(m, " @b") // a new trigger elsewhere in the line
	if m.completion == nil {
		t.Error("a new trigger elsewhere in the line should reopen")
	}
}

// TestCompletionEnterNeverSends: accepting a completion must never send
// the line, even though enter ordinarily does.
func TestCompletionEnterNeverSends(t *testing.T) {
	m, conn := completionModel()
	m = sized(m, 80, 24)
	m = typeText(m, "@al")
	m = keyEnterC(m)
	if len(conn.sent) != 0 {
		t.Errorf("accepting a completion sent %v, want nothing", conn.sent)
	}
}

// TestCompletionResizesViewport walks the viewport height through
// opening with 2 candidates, widening to 7 (5 shown), and closing,
// matching layout's h-3-(n+1) formula at 80x24 (closed: h-3=21).
func TestCompletionResizesViewport(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 24)
	if h := m.vp.Height(); h != 21 {
		t.Fatalf("closed viewport height = %d, want 21", h)
	}

	m = typeText(m, "@ali") // alice, alicia: 2 candidates
	if h := m.vp.Height(); h != 18 {
		t.Errorf("viewport height with 2 candidates = %d, want 18", h)
	}

	m = keyEsc(m)
	m = typeText(m, " @") // fresh trigger, empty term matches all 7
	if got := len(m.completion.res.Candidates); got != 7 {
		t.Fatalf("candidates for an empty term = %d, want 7", got)
	}
	if h := m.vp.Height(); h != 15 {
		t.Errorf("viewport height with 7 candidates (5 shown) = %d, want 15", h)
	}

	m = keyEsc(m)
	if h := m.vp.Height(); h != 21 {
		t.Errorf("closing did not restore the viewport height: got %d, want 21", h)
	}
}

// TestCompletionPreservesScrollPosition: opening or resizing the list
// must not yank a reader who scrolled up back to the bottom.
func TestCompletionPreservesScrollPosition(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 24)
	for i := 0; i < 60; i++ {
		m.push(item{kind: "event", text: fmt.Sprint(i)})
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.vp.AtBottom() {
		t.Fatal("pgup should have left the viewport scrolled up")
	}
	before := m.vp.YOffset()

	m = typeText(m, "@ali") // opens with 2 candidates, shrinking the viewport
	after := m.vp.YOffset()
	if after > before {
		t.Errorf("YOffset grew from %d to %d; opening the list scrolled down", before, after)
	}
	if before-after > 3 { // the list+header take at most 3 rows here
		t.Errorf("YOffset moved by %d, more than the list's own height", before-after)
	}
}

// TestCompletionCloseReclampsPastBottom: viewport.SetHeight (bubbles
// v2.2.1) does not reclamp yOffset on its own, so growing the viewport
// back when the list closes could leave it referencing rows past the
// end of the content — relayout must notice and snap back to the
// bottom rather than draw blank filler there.
func TestCompletionCloseReclampsPastBottom(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 24)
	for i := 0; i < 30; i++ {
		m.push(item{kind: "event", text: fmt.Sprint(i)})
	}
	m = typeText(m, "@ali") // opens the list, shrinking the viewport
	if m.completion == nil {
		t.Fatal("completion did not open")
	}
	m.vp.GotoBottom()
	m.vp.SetYOffset(m.vp.YOffset() - 1) // scroll up exactly one line
	if m.vp.AtBottom() {
		t.Fatal("setup: expected to be scrolled one line above the bottom")
	}

	m = keyEsc(m) // closes the list, growing the viewport back
	if m.vp.PastBottom() {
		t.Errorf("closing the list left the viewport past the bottom (yOffset %d, height %d)",
			m.vp.YOffset(), m.vp.Height())
	}
}

// TestCompletionNoTypingNoticeOnNavigation: cycling and dismissing must
// stay silent; accepting and ordinary typing still send one notice per
// keystroke/accept.
func TestCompletionNoTypingNoticeOnNavigation(t *testing.T) {
	m, conn := completionModel()
	m = sized(m, 80, 24)
	m = typeText(m, "@al")
	conn.typingNotices = 0 // ignore the notices typing "@al" itself sent

	m = keyTab(m)
	m = keyShiftTab(m)
	m = keyDown(m)
	m = keyUp(m)
	m = keyEsc(m)
	if conn.typingNotices != 0 {
		t.Errorf("navigation and dismissal sent %d typing notices, want 0", conn.typingNotices)
	}

	m = typeText(m, "hi")
	if conn.typingNotices != 2 {
		t.Errorf("typing sent %d notices, want 2 (one per keystroke)", conn.typingNotices)
	}

	m = typeText(m, " @al")
	conn.typingNotices = 0
	m = keyEnterC(m)
	if conn.typingNotices != 1 {
		t.Errorf("accept sent %d typing notices, want 1", conn.typingNotices)
	}
}

// TestCompletionNoListModeOnSmallTerminal: an 80x8 terminal cannot
// spare 3 rows for a list, so cycling previews the highlighted
// candidate in place instead of drawing a dropdown.
func TestCompletionNoListModeOnSmallTerminal(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 8)
	m = typeText(m, "@al") // alice, alicia
	if !m.noList() {
		t.Fatal("an 80x8 terminal should be in no-list mode")
	}
	if n := strings.Count(m.View().Content, "\n"); n != 8-1 {
		t.Errorf("view has %d lines, want exactly the terminal height", n+1)
	}
	if strings.Contains(plain(m.View().Content), "tab/shift+tab next") {
		t.Error("no-list mode must not draw the list header")
	}

	m = keyTab(m) // first nav key only reveals index 0 (alice)
	if got, want := m.input.Value(), "@alice"; got != want {
		t.Errorf("preview after the first tab = %q, want %q", got, want)
	}
	m = keyTab(m) // second nav key moves to index 1 (alicia)
	if got, want := m.input.Value(), "@alicia"; got != want {
		t.Errorf("preview after the second tab = %q, want %q", got, want)
	}
	m = keyEnterC(m)
	if got, want := m.input.Value(), "@alicia "; got != want {
		t.Errorf("value after accept = %q, want %q", got, want)
	}
	if m.completion != nil {
		t.Error("accept should close the completion")
	}
}

// TestCompletionHelpLineAndRows checks the ANSI-stripped view carries
// the new help entry and, with the list open, the header hint and a
// candidate row; "+N more" appears only while candidates remain below
// the visible window.
func TestCompletionHelpLineAndRows(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 24)
	if !strings.Contains(plain(m.View().Content), "tab complete") {
		t.Error("help line is missing \"tab complete\"")
	}

	m = typeText(m, "@al")
	v := plain(m.View().Content)
	if !strings.Contains(v, "tab/shift+tab next") {
		t.Error("view is missing the completion header hint")
	}
	if !strings.Contains(v, " alice") {
		t.Error("view is missing the alice candidate row")
	}
	rawLines := strings.Split(m.View().Content, "\n")
	var highlightLine string
	for _, l := range rawLines {
		if strings.Contains(plain(l), "alice") {
			highlightLine = l
			break
		}
	}
	if highlightLine == "" {
		t.Fatal("could not find the alice row in the raw view")
	}
	if w := lipgloss.Width(highlightLine); w != m.vp.Width() {
		t.Errorf("highlighted row width = %d, want %d (padded to the viewport width)",
			w, m.vp.Width())
	}

	m = keyEsc(m)
	m = typeText(m, " @") // empty term: all 7 names, 5 shown
	v = plain(m.View().Content)
	if !strings.Contains(v, "+2 more") {
		t.Errorf("view with 7 candidates should show \"+2 more\": %q", v)
	}
	// Scroll to the end of the window: the tail disappears. The window
	// is 5 wide over 7 candidates, so the highlight (and the top of the
	// window with it) has to pass the 5th slot twice before top reaches
	// its maximum (2) and every candidate is in view.
	for i := 0; i < 6; i++ {
		m = keyTab(m)
	}
	if strings.Contains(plain(m.View().Content), "more") {
		t.Error("\"+N more\" should disappear once the window reaches the end")
	}
}

// TestCompletionHelpLineFitsNarrowTerminal: the help line must not wrap
// a 40-column terminal, which would throw off the fixed row layout.
func TestCompletionHelpLineFitsNarrowTerminal(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 40, 24)
	lines := strings.Split(m.View().Content, "\n")
	help := plain(lines[len(lines)-1])
	if w := lipgloss.Width(help); w > 40 {
		t.Errorf("help line is %d cells wide, want <= 40", w)
	}
}

// TestNoConnPushesNotConnected: a nil conn (no chat client) must not
// panic on enter, and reports the same way a send error does.
func TestNoConnPushesNotConnected(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 24)
	m = typeText(m, "hello")
	m = keyEnterC(m)
	if len(m.items) != 1 {
		t.Fatalf("items = %+v, want one send-failed row", m.items)
	}
	if got := plain(m.renderItem(m.items[0])); got != "send failed: not connected" {
		t.Errorf("row = %q, want the not-connected message", got)
	}
}

// TestCompletionWindowSurvivesResize: a resize between opening the list
// and drawing it must not leave the completion window's top stale.
// Growing must not walk completionView's Candidates indexing past the
// end (the panic case); shrinking must not draw a window that excludes
// the highlight.
func TestCompletionWindowSurvivesResize(t *testing.T) {
	// Grow: 80x10 gives 4 rows over 7 candidates (see listRows), so
	// cycling to the last one leaves top=3, index=6. Growing to 80x24
	// (5 rows) must reclamp top rather than leave completionView
	// reading Candidates[3..7], one past the end.
	grow, _ := completionModel()
	grow = sized(grow, 80, 10)
	grow = typeText(grow, "@") // empty term: all 7 names
	if got := len(grow.completion.res.Candidates); got != 7 {
		t.Fatalf("candidates for a bare @ = %d, want all 7", got)
	}
	for i := 0; i < 6; i++ {
		grow = keyTab(grow)
	}
	if grow.completion.index != 6 || grow.completion.top != 3 {
		t.Fatalf("state before resize = index %d top %d, want index 6 top 3 "+
			"(the exact repro this test guards)", grow.completion.index, grow.completion.top)
	}

	// Check top/index right after the resize, before ever calling
	// View(): completionView no longer clamps (it must stay read-only),
	// so this pins layout's own clamp rather than the drawing code
	// papering over a state View() itself left inconsistent.
	grow = sized(grow, 80, 24)
	rows := grow.listRows()
	if grow.completion.top < 0 || grow.completion.top > 7-rows {
		t.Fatalf("top = %d out of range for rows=%d over 7 candidates",
			grow.completion.top, rows)
	}
	if idx := grow.completion.index; idx < grow.completion.top || idx >= grow.completion.top+rows {
		t.Errorf("highlight (index %d) outside the drawn window [%d, %d)",
			idx, grow.completion.top, grow.completion.top+rows)
	}

	view := plain(grow.View().Content)    // must not panic
	if !strings.Contains(view, "frank") { // alphabetically last, the index-6 candidate
		t.Errorf("view is missing the highlighted candidate's row: %q", view)
	}

	// Shrink: the mirror case, cycling to the end at 80x24 then
	// shrinking to 80x10 must still draw the highlight in view. Same
	// order: check the state layout produced before drawing it.
	shrink, _ := completionModel()
	shrink = sized(shrink, 80, 24)
	shrink = typeText(shrink, "@")
	for i := 0; i < 6; i++ {
		shrink = keyTab(shrink)
	}
	shrink = sized(shrink, 80, 10)
	rows = shrink.listRows()
	if idx := shrink.completion.index; idx < shrink.completion.top || idx >= shrink.completion.top+rows {
		t.Errorf("after shrinking, highlight (index %d) outside the drawn window [%d, %d)",
			idx, shrink.completion.top, shrink.completion.top+rows)
	}
	_ = shrink.View().Content // must not panic
}

// TestCompletionHighlightIsOneStyledRun: a Dim label plus a Detail must
// not break the pick highlight into styled/unstyled/styled bands. Every
// non-reset SGR code on the active row must be the same code (pick's),
// which is only true if the row was stripped of its own Dim/Detail
// styling before pick wrapped it. Unhighlighted rows keep their
// separate styling, unchanged.
func TestCompletionHighlightIsOneStyledRun(t *testing.T) {
	m := newModel()
	cand := complete.Candidate{Label: "alice", Detail: "some detail", Dim: true}

	active := m.completionRow(cand, true, 40)
	opens := distinctSGROpens(active)
	if len(opens) != 1 {
		t.Errorf("highlighted row carries %d distinct style codes, want 1 (pick only): %v\nraw: %q",
			len(opens), opens, active)
	}
	if got := plain(active); !strings.Contains(got, "alice") || !strings.Contains(got, "some detail") {
		t.Errorf("row text = %q, missing the label or the detail", got)
	}

	// away and help happen to be identical (both plain Faint) in the
	// default theme, so this checks run *count*, not distinct codes: two
	// separate Render calls (label, then detail) must still show up as
	// two separate open/reset pairs rather than being collapsed or lost.
	inactive := m.completionRow(cand, false, 40)
	if n := len(sgrCode.FindAllString(inactive, -1)); n < 4 {
		t.Errorf("unhighlighted row lost its separate Dim/Detail styling "+
			"(%d SGR codes, want at least 4: two open/reset pairs): %q", n, inactive)
	}
}

// sgrCode matches one SGR escape and captures its parameter list, which
// is empty or "0" for a reset and non-empty for an opening style.
var sgrCode = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// distinctSGROpens returns the set of distinct non-reset SGR parameter
// strings in s, so a caller can tell whether every styled span shares
// one style (padding re-opens the same code, which counts as one) or
// several different ones leaked through.
func distinctSGROpens(s string) []string {
	seen := map[string]bool{}
	var opens []string
	for _, m := range sgrCode.FindAllStringSubmatch(s, -1) {
		code := m[1]
		if code == "" || code == "0" {
			continue
		}
		if !seen[code] {
			seen[code] = true
			opens = append(opens, code)
		}
	}
	return opens
}

// TestAcceptCompletionRespectsCharLimit: accepting must never silently
// truncate the user's own text off the end of the line to make room.
// Over the limit, the splice is refused, the list simply closes (not
// dismissed — a dismissal only clears once the span's start moves, and
// backspacing inside the word never does that), and an error row
// explains why — with no typing notice, since nothing was sent.
func TestAcceptCompletionRespectsCharLimit(t *testing.T) {
	m, conn := completionModel()
	m = sized(m, 80, 24)
	limit := m.input.CharLimit
	// "@al" sits at the very end, right after a space (the stub
	// trigger's \B needs a non-word character there); the filler brings
	// the line to exactly the limit, so accepting ("@al" -> "@alice ")
	// can only grow it.
	value := strings.Repeat("y", limit-4) + " @al"
	m.input.SetValue(value)
	m.input.SetCursor(len([]rune(value)))
	m.recompute()
	if m.completion == nil {
		t.Fatal("completion did not open")
	}

	m = keyEnterC(m)
	if got := m.input.Value(); got != value {
		t.Errorf("value changed despite exceeding the char limit:\nvalue changed to len %d, want unchanged at %d",
			len([]rune(got)), len([]rune(value)))
	}
	if m.completion != nil {
		t.Error("a refused accept should still close the list")
	}
	if conn.typingNotices != 0 {
		t.Errorf("typing notices = %d, want 0 (the accept was refused)", conn.typingNotices)
	}
	if len(m.items) != 1 {
		t.Fatalf("items = %+v, want one error row", m.items)
	}
	want := fmt.Sprintf("completion would exceed the %d-character limit", limit)
	if got := plain(m.renderItem(m.items[0])); got != want {
		t.Errorf("row = %q, want %q", got, want)
	}

	// Not dismissed: the refusal only closed the list, so backspacing
	// inside the word (removing a character to make room, without
	// moving the span's start) reopens it on the very next keystroke.
	m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.completion == nil {
		t.Error("a backspace that makes room should reopen the list, not stay closed")
	}
}

// TestWritePreviewRespectsCharLimit: no-list mode's preview must skip
// rather than truncate the user's line when it would cross CharLimit,
// leaving the list open so a shorter candidate can still be tried.
func TestWritePreviewRespectsCharLimit(t *testing.T) {
	m, _ := completionModel()
	m = sized(m, 80, 8) // no-list mode: not enough rows to spare a list
	limit := m.input.CharLimit
	value := strings.Repeat("y", limit-4) + " @al"
	m.input.SetValue(value)
	m.input.SetCursor(len([]rune(value)))
	m.recompute()
	if m.completion == nil {
		t.Fatal("completion did not open")
	}
	if !m.noList() {
		t.Fatal("setup: expected no-list mode at 80x8, is the layout unchanged?")
	}

	m = keyTab(m) // first nav key in no-list mode previews index 0 (alice)
	if got := m.input.Value(); got != value {
		t.Errorf("writePreview wrote past the char limit: value changed to len %d, want unchanged at %d",
			len([]rune(got)), len([]rune(value)))
	}
	if m.completion == nil {
		t.Error("the guard should leave the completion open, not close it")
	}
}

// TestWritePreviewChecksFullInsertWithSpace: a candidate that fits
// without its trailing space but not with it could never actually be
// accepted, so it must never be shown as a preview either — the check
// has to be against the full Insert, not the trimmed text writePreview
// itself writes.
func TestWritePreviewChecksFullInsertWithSpace(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 8) // no-list mode
	// Two candidates, both prefixed "car", "carol" ranking first
	// alphabetically — two, so tab cycles (writePreview) rather than
	// taking the single-candidate accept path.
	m.sources = []complete.Source{stubMentionSource{names: []string{"carol", "carolyn"}}}

	limit := m.input.CharLimit
	// Sized so the span ("@car", 4 bytes) swapped for "@carol" (6, no
	// space) lands exactly at the limit, but "@carol " (7, with the
	// space Insert actually carries) is one over.
	value := strings.Repeat("y", limit-7) + " @car"
	m.input.SetValue(value)
	m.input.SetCursor(len([]rune(value)))
	m.recompute()
	if m.completion == nil {
		t.Fatal("completion did not open")
	}
	if !m.noList() {
		t.Fatal("setup: expected no-list mode at 80x8")
	}
	if got := m.completion.res.Candidates; len(got) != 2 || got[0].Label != "carol" {
		t.Fatalf("candidates = %+v, want carol first of two", got)
	}

	m = keyTab(m) // first nav key: reveal index 0 (carol) — or refuse
	if got := m.input.Value(); got != value {
		t.Errorf("tab wrote a preview that could never be accepted: "+
			"value changed to len %d, want unchanged at %d",
			len([]rune(got)), len([]rune(value)))
	}
	if m.completion == nil {
		t.Error("a refused preview should leave the completion open")
	}
}

// TestTabIsNoOpWithoutCompletion: tab and shift+tab carry no printable
// text (Key.Text is empty), so with no list open they must not reach
// the textinput or fire a typing notice for an edit that never happens.
func TestTabIsNoOpWithoutCompletion(t *testing.T) {
	m, conn := completionModel()
	m = sized(m, 80, 24)
	before := m.input.Value()

	m = keyTab(m)
	m = keyShiftTab(m)
	if got := m.input.Value(); got != before {
		t.Errorf("value changed to %q from a bare tab/shift+tab", got)
	}
	if conn.typingNotices != 0 {
		t.Errorf("typing notices = %d, want 0", conn.typingNotices)
	}
	if m.completion != nil {
		t.Error("tab with no sources matching should not open a completion")
	}
}

// candidateLabels collects the labels of an open completion's
// candidates, in display order, for comparing against a wanted roster.
func candidateLabels(m Model) []string {
	if m.completion == nil {
		return nil
	}
	labels := make([]string, len(m.completion.res.Candidates))
	for i, c := range m.completion.res.Candidates {
		labels[i] = c.Label
	}
	return labels
}

// TestMentionCompletionFollowsRoster is the phase 1 wiring test: with
// no sources override, completionSources builds complete.Mentions over
// the live model, so @ opens straight from the roster and tracks joins,
// leaves and away changes without any hook into the completion engine.
func TestMentionCompletionFollowsRoster(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 24)
	m.handleEvent(client.Event{Msg: protocol.Authenticated{Username: "me"}})
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "alice"}, {UserID: 2, Username: "bob"},
		{UserID: 3, Username: "me"},
	}}})

	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"alice", "bob"}; !slices.Equal(got, want) {
		t.Fatalf("candidates after @ = %v, want %v (self excluded)", got, want)
	}

	m.handleEvent(joined(4, "carol", false))
	m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if m.completion != nil {
		t.Fatal("backspacing the @ should close the completion")
	}
	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"alice", "bob", "carol"}; !slices.Equal(got, want) {
		t.Fatalf("candidates after carol joined = %v, want %v", got, want)
	}

	m.handleEvent(client.Event{Msg: protocol.UserLeft{UserID: 2, Username: "bob"}})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"alice", "carol"}; !slices.Equal(got, want) {
		t.Fatalf("candidates after bob left = %v, want %v", got, want)
	}

	m.handleEvent(client.Event{Msg: protocol.Away{UserID: 1, Username: "alice",
		IsAway: true, AwayMessage: "lunch"}})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = typeText(m, "@")
	idx := -1
	for i, c := range m.completion.res.Candidates {
		if c.Label == "alice" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("alice missing from candidates after Away")
	}
	cand := m.completion.res.Candidates[idx]
	if !cand.Dim {
		t.Error("an away user's candidate must be Dim")
	}
	if !strings.Contains(plain(m.View().Content), "away") {
		t.Error(`view does not show "away" for the away candidate`)
	}
}

// TestMentionCompletionSelfNeverStale guards the closure-freshness
// requirement in AGENTS.md's internal/ui paragraph, in two halves.
// completionSources must build complete.Mentions bound to the current
// *Model on every call, not close over the model as it stood in New:
// a closure captured that early would trip the first assertion before
// the test ever reaches the second, since Subscribed replaces m.users
// wholesale and the captured copy's map would still be the empty one
// New made (an empty roster, not "alice, me"). The second assertion is
// what isolates the self half: even a fix that rebuilds the source
// fresh but still reads a self captured at New's time would keep
// showing "me" as a candidate after Authenticated arrives.
func TestMentionCompletionSelfNeverStale(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 24)
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "alice"}, {UserID: 2, Username: "me"},
	}}})

	// self is still "" here, so the roster's own "me" row is a normal
	// candidate — this is what a stale closure over an empty self would
	// keep showing forever.
	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"alice", "me"}; !slices.Equal(got, want) {
		t.Fatalf("candidates before auth = %v, want %v", got, want)
	}

	m.handleEvent(client.Event{Msg: protocol.Authenticated{Username: "me"}})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"alice"}; !slices.Equal(got, want) {
		t.Fatalf("candidates after auth = %v, want %v (me now excluded)", got, want)
	}
}

// TestMentionCompletionAccepts checks the end-to-end splice: accepting
// a roster-backed mention candidate inserts "@name " with the cursor at
// the end, same as the stubbed phase 0 tests already check for a
// synthetic source.
func TestMentionCompletionAccepts(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 24)
	m.conn = &fakeConn{}
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "alice"},
	}}})

	m = typeText(m, "hi @al")
	m = keyEnterC(m)
	if got, want := m.input.Value(), "hi @alice "; got != want {
		t.Fatalf("input after accept = %q, want %q", got, want)
	}
	if pos := m.input.Position(); pos != len([]rune(m.input.Value())) {
		t.Errorf("cursor after accept = %d, want end of line (%d)",
			pos, len([]rune(m.input.Value())))
	}
}

// TestMentionCompletionDropsBlankNames: a username that sanitizes to ""
// (all control/bidi characters, or an empty one off the wire) must not
// reach the candidate list, since accepting it would splice a bare
// "@ " into the line.
func TestMentionCompletionDropsBlankNames(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 24)
	m.handleEvent(client.Event{Msg: protocol.Authenticated{Username: "me"}})
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "\u200e"}, {UserID: 2, Username: ""},
		{UserID: 3, Username: "ok"},
	}}})

	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"ok"}; !slices.Equal(got, want) {
		t.Fatalf("candidates with blank-name rows = %v, want %v", got, want)
	}
}

// TestMentionCompletionSelfComparedSanitized: self and the roster are
// compared in the same sanitized form, so a stripped rune in the
// signed-in name cannot leak self into the list.
func TestMentionCompletionSelfComparedSanitized(t *testing.T) {
	m := newModel()
	m = sized(m, 80, 24)
	m.handleEvent(client.Event{Msg: protocol.Authenticated{Username: "m\u200ee"}})
	m.handleEvent(client.Event{Msg: protocol.Subscribed{UserList: []protocol.ChannelUser{
		{UserID: 1, Username: "alice"}, {UserID: 2, Username: "m\u200ee"},
	}}})

	m = typeText(m, "@")
	if got, want := candidateLabels(m), []string{"alice"}; !slices.Equal(got, want) {
		t.Fatalf("candidates = %v, want %v (self must be excluded)", got, want)
	}
}
