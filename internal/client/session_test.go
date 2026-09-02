package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
)

// countingMinter hands out numbered tokens and can be told to fail.
type countingMinter struct {
	calls atomic.Int32
	fail  atomic.Bool
}

func (m *countingMinter) Mint(context.Context) (string, error) {
	n := m.calls.Add(1)
	if m.fail.Load() {
		return "", errors.New("mint down")
	}
	return "tok" + string(rune('0'+n)), nil
}

// fakeServer scripts one socket: it walks the client through
// authenticate → channelID → subscribed, then hands control to script,
// which returns the close reason to end with. Pings are answered and
// otherwise ignored so the heartbeat never confuses a script.
type fakeServer struct {
	t      *testing.T
	script func(conn *websocket.Conn, next nextFn) string
	url    string
}

// nextFn reads the next non-ping client frame, or nil once ctx is done
// or the socket closes. Scripts pass a short-deadline ctx to assert that
// nothing arrives.
type nextFn func(ctx context.Context) map[string]any

func newFakeServer(t *testing.T, script func(*websocket.Conn,
	nextFn) string) *fakeServer {
	fs := &fakeServer{t: t, script: script}
	srv := httptest.NewServer(http.HandlerFunc(fs.handle))
	t.Cleanup(srv.Close)
	fs.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return fs
}

func (fs *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// A dedicated reader feeds frames through a channel so that next can
	// give up on a deadline without touching the socket: cancelling a
	// coder/websocket Read closes the connection, which would turn "the
	// client stayed quiet" into "the server hung up".
	frames := make(chan map[string]any, 16)
	go func() {
		defer close(frames)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				continue
			}
			if m["name"] == "ping" {
				_ = send(ctx, conn, map[string]any{"name": "pong",
					"data": m["data"], "serverTime": 1})
				continue
			}
			frames <- m
		}
	}()
	next := func(readCtx context.Context) map[string]any {
		select {
		case m := <-frames:
			return m
		case <-readCtx.Done():
			return nil
		}
	}

	if m := next(ctx); m == nil || m["name"] != "authenticate" {
		fs.t.Errorf("first frame = %v, want authenticate", m)
		conn.Close(websocket.StatusProtocolError, "bad handshake")
		return
	}
	_ = send(ctx, conn, map[string]any{"name": "authenticated",
		"username": "bob", "userID": 7, "motd": "", "serverTime": 1})
	if m := next(ctx); m == nil || m["name"] != "getChannelID" {
		fs.t.Errorf("frame = %v, want getChannelID", m)
		return
	}
	_ = send(ctx, conn, map[string]any{"name": "channelID",
		"channelID": 3, "channelName": "general", "serverTime": 1})
	if m := next(ctx); m == nil || m["name"] != "subscribe" {
		fs.t.Errorf("frame = %v, want subscribe", m)
		return
	}
	_ = send(ctx, conn, map[string]any{"name": "subscribed",
		"channelID": 3, "messageBuffer": []any{}, "serverTime": 1})

	reason := fs.script(conn, next)
	conn.Close(websocket.StatusNormalClosure, reason)
}

func send(ctx context.Context, conn *websocket.Conn, v any) error {
	data, _ := json.Marshal(v)
	return conn.Write(ctx, websocket.MessageText, data)
}

// runUntilStopped runs the client against the fake server and collects
// every event until Run returns.
func runUntilStopped(t *testing.T, fs *fakeServer, m *countingMinter) []Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := New(Config{WSURL: fs.url, Channel: "general", Minter: m})
	go c.Run(ctx)
	var events []Event
	for e := range c.Events() {
		events = append(events, e)
	}
	return events
}

func countState(events []Event, s State) int {
	n := 0
	for _, e := range events {
		if e.State == s {
			n++
		}
	}
	return n
}

// TestRevalidateRenewsInPlace is the whole point of ngchat-cli#1: a
// revalidate nudge produces exactly one reauthenticate with a fresh token,
// the revalidated ack reaches the UI, and no reconnect happens.
func TestRevalidateRenewsInPlace(t *testing.T) {
	fs := newFakeServer(t, func(conn *websocket.Conn, next nextFn) string {
		ctx := context.Background()
		_ = send(ctx, conn, map[string]any{"name": "revalidate", "serverTime": 1})
		m := next(ctx)
		if m == nil || m["name"] != "reauthenticate" {
			t.Errorf("after revalidate got %v, want reauthenticate", m)
			return "server close"
		}
		if m["token"] != "tok2" {
			t.Errorf("reauthenticate token = %v, want a fresh mint (tok2)",
				m["token"])
		}
		_ = send(ctx, conn, map[string]any{"name": "revalidated",
			"isAdmin": false, "isChatMod": true, "isSiteMod": false,
			"serverTime": 1})
		// Give the client a moment to surface the ack before closing.
		time.Sleep(50 * time.Millisecond)
		return "server close"
	})

	minter := &countingMinter{}
	events := runUntilStopped(t, fs, minter)

	if got := minter.calls.Load(); got != 2 {
		t.Errorf("mint calls = %d, want 2 (connect + renewal)", got)
	}
	if countState(events, StateReconnecting) != 0 {
		t.Error("renewal must not trigger a reconnect")
	}
	var acked bool
	for _, e := range events {
		if r, ok := e.Msg.(protocol.Revalidated); ok {
			acked = true
			if !r.IsChatMod {
				t.Error("revalidated flags not delivered")
			}
		}
	}
	if !acked {
		t.Error("revalidated ack never reached the event stream")
	}
	last := events[len(events)-1]
	if last.State != StateStopped || last.Err == nil ||
		last.Err.Error() != "server close" {
		t.Errorf("final event = %+v, want stopped on server close", last)
	}
}

// TestRevalidateMintFailureIsNotFatal: a failed re-mint must surface as an
// event and leave the session running — the server's close timer still
// arms the ordinary reconnect path.
func TestRevalidateMintFailureIsNotFatal(t *testing.T) {
	minter := &countingMinter{}
	fs := newFakeServer(t, func(conn *websocket.Conn, next nextFn) string {
		ctx := context.Background()
		minter.fail.Store(true)
		_ = send(ctx, conn, map[string]any{"name": "revalidate", "serverTime": 1})
		// The client must not answer; prove the socket is still alive by
		// pushing a chat line through it afterwards.
		time.Sleep(50 * time.Millisecond)
		_ = send(ctx, conn, map[string]any{"name": "message", "id": 1,
			"channelID": 3, "message": "still here", "username": "bob"})
		quiet, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		m := next(quiet)
		if m != nil && m["name"] == "reauthenticate" {
			t.Error("client sent reauthenticate despite a failed mint")
		}
		return "server close"
	})

	events := runUntilStopped(t, fs, minter)

	var failed, gotMessage bool
	for _, e := range events {
		switch e.Msg.(type) {
		case RenewalFailed:
			failed = true
		case protocol.Message:
			gotMessage = true
		}
	}
	if !failed {
		t.Error("RenewalFailed event not emitted")
	}
	if !gotMessage {
		t.Error("session did not keep reading after the failed renewal")
	}
	if countState(events, StateReconnecting) != 0 {
		t.Error("failed renewal must not trigger a reconnect by itself")
	}
}

// TestKickReasonAndIdleReason pin the stop-error text the UI shows.
func TestStopReasons(t *testing.T) {
	tests := []struct {
		name   string
		frames []map[string]any
		close  string
		want   string
	}{
		{"kicked with reason",
			[]map[string]any{{"name": "kicked", "reason": "spam", "serverTime": 1}},
			"server close", "kicked: spam"},
		{"kicked without reason",
			[]map[string]any{{"name": "kicked", "serverTime": 1}},
			"server close", "kicked"},
		{"idle timeout carries the server's explanation",
			[]map[string]any{{"name": "idleTimeout",
				"reason": "no activity for 24h", "serverTime": 1}},
			"idle timeout", "idle timeout: no activity for 24h"},
		{"idle timeout close without a preceding frame",
			nil, "idle timeout", "idle timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t, func(conn *websocket.Conn, _ nextFn) string {
				for _, f := range tc.frames {
					_ = send(context.Background(), conn, f)
				}
				time.Sleep(50 * time.Millisecond)
				return tc.close
			})
			events := runUntilStopped(t, fs, &countingMinter{})
			last := events[len(events)-1]
			if last.Err == nil || last.Err.Error() != tc.want {
				t.Errorf("stop error = %v, want %q", last.Err, tc.want)
			}
		})
	}
}
