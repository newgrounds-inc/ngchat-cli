package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
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
