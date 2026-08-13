package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

// newModel builds a Model with no chat client; the tests here never send, and
// the transcript renders lazily so no viewport is needed.
func newModel() Model { return New(nil, "general") }

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
			})
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
	})

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
		Message: "hi"})
	if got := plain(m.renderItem(m.items[0])); got != "<bob> hi" {
		t.Errorf("chat row = %q", got)
	}

	m.pushMessage(protocol.Message{Name: "directMessage", Username: "bob",
		Message: "psst"})
	if got := plain(m.renderItem(m.items[1])); !strings.HasPrefix(got, "[DM] ") {
		t.Errorf("dm row = %q, want a [DM] prefix", got)
	}

	m.pushMessage(protocol.Message{Name: "meMessage", Username: "bob",
		Message: "waves"})
	if got := plain(m.renderItem(m.items[2])); got != "* bob waves" {
		t.Errorf("me row = %q", got)
	}
}

// TestRenderItemConvertsHTML confirms the transcript goes through the render
// package rather than printing raw markup.
func TestRenderItemConvertsHTML(t *testing.T) {
	m := newModel()
	m.pushMessage(protocol.Message{Name: "message", Username: "bob",
		Message: `say <strong>hi</strong> to <a href="https://x.test">x</a>`})

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
	if len(m.items) != 0 {
		t.Errorf("an empty MOTD should not push a row: %+v", m.items)
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
