package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
)

// countingMinter hands out numbered tokens and can be told to fail, to
// repeat the last token (a cached mint), or to take a while.
type countingMinter struct {
	calls     atomic.Int32
	fail      atomic.Bool
	signedOut atomic.Bool
	repeat    atomic.Bool
	delay     time.Duration
}

func (m *countingMinter) Mint(ctx context.Context) (string, error) {
	n := m.calls.Add(1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if m.signedOut.Load() {
		return "", auth.ErrSignedOut
	}
	if m.fail.Load() {
		return "", errors.New("mint down")
	}
	if m.repeat.Load() && n > 1 {
		n--
	}
	return "tok" + strconv.Itoa(int(n)), nil
}

// nextFn reads the next non-ping client frame, or nil once ctx is done
// or the socket closes. Scripts pass a short-deadline ctx to assert that
// nothing arrives.
type nextFn func(ctx context.Context) map[string]any

// script drives one connection after the standard handshake. attempt is
// 1 for the first connection and climbs on each reconnect. It returns
// the close reason to end with.
type script func(conn *websocket.Conn, attempt int, next nextFn) string

// fakeServer walks every connection through authenticate → channelID →
// subscribed, then hands control to the script. Pings are answered and
// otherwise hidden so the heartbeat never confuses a script.
type fakeServer struct {
	t        *testing.T
	script   script
	url      string
	attempts atomic.Int32
	wg       sync.WaitGroup
}

func newFakeServer(t *testing.T, s script) *fakeServer {
	fs := &fakeServer{t: t, script: s}
	srv := httptest.NewServer(http.HandlerFunc(fs.handle))
	// httptest.Server.Close does not wait for hijacked connections, so
	// hold the test open until every handler (which may call t.Errorf)
	// has returned.
	t.Cleanup(func() {
		srv.Close()
		fs.wg.Wait()
	})
	fs.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return fs
}

func (fs *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	fs.wg.Add(1)
	defer fs.wg.Done()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	attempt := int(fs.attempts.Add(1))

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

	reason := fs.script(conn, attempt, next)
	conn.Close(websocket.StatusNormalClosure, reason)
}

func send(ctx context.Context, conn *websocket.Conn, v any) error {
	data, _ := json.Marshal(v)
	return conn.Write(ctx, websocket.MessageText, data)
}

// quietFor asserts that the client sends no frame within d.
func quietFor(t *testing.T, next nextFn, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if m := next(ctx); m != nil {
		t.Errorf("client sent %v, want silence", m)
	}
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
	if len(events) == 0 {
		t.Fatal("client emitted no events")
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

func hasMsg[T any](events []Event) bool {
	for _, e := range events {
		if _, ok := e.Msg.(T); ok {
			return true
		}
	}
	return false
}

// TestRevalidateRenewsInPlace is the whole point of ngchat-cli#1: a
// revalidate nudge produces exactly one reauthenticate with a fresh token,
// the revalidated ack reaches the UI, and no reconnect happens.
func TestRevalidateRenewsInPlace(t *testing.T) {
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
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

// TestRevalidateIsAnsweredOnceUntilAcked: nudges that arrive while a
// renewal is unacked must not mint again, but an acked renewal may be
// followed immediately — a short-TTL token nudges right after every ack,
// and the live check depends on chained renewals staying in place.
func TestRevalidateIsAnsweredOnceUntilAcked(t *testing.T) {
	nudge := map[string]any{"name": "revalidate", "serverTime": 1}
	ack := map[string]any{"name": "revalidated", "isAdmin": false,
		"isChatMod": false, "isSiteMod": false, "serverTime": 1}
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
		ctx := context.Background()
		for range 3 {
			_ = send(ctx, conn, nudge)
		}
		if m := next(ctx); m == nil || m["name"] != "reauthenticate" {
			t.Errorf("got %v, want one reauthenticate", m)
		}
		quietFor(t, next, 200*time.Millisecond)

		_ = send(ctx, conn, ack)
		_ = send(ctx, conn, nudge)
		if m := next(ctx); m == nil || m["token"] != "tok3" {
			t.Errorf("after ack got %v, want a second renewal (tok3)", m)
		}
		return "server close"
	})

	minter := &countingMinter{}
	runUntilStopped(t, fs, minter)
	if got := minter.calls.Load(); got != 3 {
		t.Errorf("mint calls = %d, want 3 (connect + two renewals)", got)
	}
}

// TestRevalidateBudget caps renewals per minute even when every one is
// acked, so a server nudging in a loop cannot drive the site's limiter.
func TestRevalidateBudget(t *testing.T) {
	nudge := map[string]any{"name": "revalidate", "serverTime": 1}
	ack := map[string]any{"name": "revalidated", "isAdmin": false,
		"isChatMod": false, "isSiteMod": false, "serverTime": 1}
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
		ctx := context.Background()
		for i := range maxRenewalsPerMinute {
			_ = send(ctx, conn, nudge)
			if m := next(ctx); m == nil || m["name"] != "reauthenticate" {
				t.Errorf("renewal %d: got %v", i+1, m)
			}
			_ = send(ctx, conn, ack)
		}
		_ = send(ctx, conn, nudge)
		quietFor(t, next, 200*time.Millisecond)
		return "server close"
	})

	minter := &countingMinter{}
	runUntilStopped(t, fs, minter)
	if got := minter.calls.Load(); got != int32(1+maxRenewalsPerMinute) {
		t.Errorf("mint calls = %d, want %d", got, 1+maxRenewalsPerMinute)
	}
}

// TestSlowMintKeepsSession: the mint runs on the read goroutine, so a
// slow one must delay frames, not lose them or drop the socket.
func TestSlowMintKeepsSession(t *testing.T) {
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
		ctx := context.Background()
		_ = send(ctx, conn, map[string]any{"name": "revalidate", "serverTime": 1})
		_ = send(ctx, conn, map[string]any{"name": "message", "id": 1,
			"channelID": 3, "message": "queued behind the mint", "username": "bob"})
		if m := next(ctx); m == nil || m["name"] != "reauthenticate" {
			t.Errorf("got %v, want reauthenticate", m)
		}
		return "server close"
	})

	minter := &countingMinter{delay: 150 * time.Millisecond}
	events := runUntilStopped(t, fs, minter)
	if !hasMsg[protocol.Message](events) {
		t.Error("frame sent during the mint was lost")
	}
	if countState(events, StateReconnecting) != 0 {
		t.Error("slow mint must not cause a reconnect")
	}
}

// TestRevalidateMintFailureIsNotFatal: a failed re-mint must surface as an
// event and leave the session running — the server's close timer still
// arms the ordinary reconnect path.
func TestRevalidateMintFailureIsNotFatal(t *testing.T) {
	minter := &countingMinter{}
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
		ctx := context.Background()
		minter.fail.Store(true)
		_ = send(ctx, conn, map[string]any{"name": "revalidate", "serverTime": 1})
		quietFor(t, next, 200*time.Millisecond)
		// Prove the socket is still alive by pushing a chat line through.
		_ = send(ctx, conn, map[string]any{"name": "message", "id": 1,
			"channelID": 3, "message": "still here", "username": "bob"})
		return "server close"
	})

	events := runUntilStopped(t, fs, minter)

	if !hasMsg[RenewalFailed](events) {
		t.Error("RenewalFailed event not emitted")
	}
	if !hasMsg[protocol.Message](events) {
		t.Error("session did not keep reading after the failed renewal")
	}
	if countState(events, StateReconnecting) != 0 {
		t.Error("failed renewal must not trigger a reconnect by itself")
	}
}

// TestRevalidateRefusesRepeatedToken: the server drops a socket whose
// renewal token is not strictly newer, so a cached mint is withheld.
func TestRevalidateRefusesRepeatedToken(t *testing.T) {
	minter := &countingMinter{}
	minter.repeat.Store(true)
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
		_ = send(context.Background(), conn,
			map[string]any{"name": "revalidate", "serverTime": 1})
		quietFor(t, next, 200*time.Millisecond)
		return "server close"
	})

	events := runUntilStopped(t, fs, minter)
	if !hasMsg[RenewalFailed](events) {
		t.Error("a repeated token should be reported as a failed renewal")
	}
}

// TestExpiryReconnects pins the server's real expiry sequence: an
// "unauthorized: jwt expired" frame, then a close with reason "token
// expired". After authentication the client must not answer the frame
// (authenticate would trip the already-authenticated guard) and must
// reconnect on the close.
func TestExpiryReconnects(t *testing.T) {
	fs := newFakeServer(t, func(conn *websocket.Conn, attempt int, next nextFn) string {
		if attempt > 1 {
			return "server close"
		}
		_ = send(context.Background(), conn, map[string]any{
			"name": "unauthorized", "message": "jwt expired", "serverTime": 1})
		quietFor(t, next, 200*time.Millisecond)
		return "token expired"
	})

	minter := &countingMinter{}
	events := runUntilStopped(t, fs, minter)

	if got := fs.attempts.Load(); got != 2 {
		t.Errorf("connections = %d, want 2 (expiry then reconnect)", got)
	}
	if countState(events, StateReconnecting) != 1 {
		t.Error("expected exactly one reconnecting event")
	}
	if got := minter.calls.Load(); got != 2 {
		t.Errorf("mint calls = %d, want 2 (one per connection)", got)
	}
}

// TestStopReasons pins the stop-error text the UI shows.
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
		{"idle timeout is final on the frame, whatever the close says",
			[]map[string]any{{"name": "idleTimeout",
				"reason": "no activity for 24h", "serverTime": 1}},
			"going away", "idle timeout: no activity for 24h"},
		{"idle timeout close without a preceding frame",
			nil, "idle timeout", "idle timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
				for _, f := range tc.frames {
					_ = send(context.Background(), conn, f)
				}
				// Let the frame reach the client before the close does.
				quietFor(t, next, 50*time.Millisecond)
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

// TestSignedOutOnConnectStops: a 401 from the site on the connect mint is
// final. No dial, no backoff, no reconnect; the stop error wraps
// auth.ErrSignedOut so the UI can say "run ngchat login".
func TestSignedOutOnConnectStops(t *testing.T) {
	fs := newFakeServer(t, func(*websocket.Conn, int, nextFn) string {
		return "server close"
	})
	minter := &countingMinter{}
	minter.signedOut.Store(true)
	events := runUntilStopped(t, fs, minter)
	last := events[len(events)-1]
	if last.State != StateStopped || !errors.Is(last.Err, auth.ErrSignedOut) {
		t.Errorf("last event = %+v, want StateStopped wrapping ErrSignedOut", last)
	}
	if countState(events, StateReconnecting) != 0 {
		t.Error("signed out must not reconnect")
	}
	if fs.attempts.Load() != 0 || minter.calls.Load() != 1 {
		t.Errorf("attempts = %d, mints = %d; want 0 and 1",
			fs.attempts.Load(), minter.calls.Load())
	}
}

// TestSignedOutOnRevalidateStops: the same 401 during a renewal ends the
// session at once instead of surfacing as a RenewalFailed and riding the
// old token to its deadline.
func TestSignedOutOnRevalidateStops(t *testing.T) {
	minter := &countingMinter{}
	fs := newFakeServer(t, func(conn *websocket.Conn, attempt int, next nextFn) string {
		if attempt > 1 {
			t.Error("reconnected after being signed out")
			return "server close"
		}
		minter.signedOut.Store(true)
		ctx := context.Background()
		_ = send(ctx, conn, map[string]any{"name": "revalidate", "serverTime": 1})
		quietFor(t, next, 300*time.Millisecond)
		return ""
	})
	events := runUntilStopped(t, fs, minter)
	last := events[len(events)-1]
	if last.State != StateStopped || !errors.Is(last.Err, auth.ErrSignedOut) {
		t.Errorf("last event = %+v, want StateStopped wrapping ErrSignedOut", last)
	}
	if hasMsg[RenewalFailed](events) {
		t.Error("signed out must stop, not report a retryable renewal failure")
	}
}
