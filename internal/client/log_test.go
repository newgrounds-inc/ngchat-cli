package client

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRedact(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"authenticate token",
			`{"name":"authenticate","token":"eyJhbGciOi.payload.sig"}`,
			`{"name":"authenticate","token":"[redacted]"}`},
		{"spaced token key",
			`{"token" : "abc"}`, `{"token" : "[redacted]"}`},
		{"cookie header",
			"Cookie: ng_remember=abc123; newgrounds_session=s3; XSRF-TOKEN=x%3D; serverid=backend",
			"Cookie: ng_remember=[redacted]; newgrounds_session=[redacted]; XSRF-TOKEN=[redacted]; serverid=[redacted]"},
		{"cookie in error text",
			`mint: 401 for ng_remember=abc`,
			`mint: 401 for ng_remember=[redacted]`},
		{"chat text untouched",
			`{"name":"message","message":"my token=value is fine"}`,
			`{"name":"message","message":"my token=value is fine"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Redact(tc.in); got != tc.want {
				t.Errorf("Redact(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDebugLogNeverCarriesTheToken drives a full session with logging on
// and checks that the outbound authenticate and reauthenticate frames
// reach the log with their tokens masked, while heartbeats stay out.
func TestDebugLogNeverCarriesTheToken(t *testing.T) {
	fs := newFakeServer(t, func(conn *websocket.Conn, _ int, next nextFn) string {
		ctx := context.Background()
		_ = send(ctx, conn, map[string]any{"name": "revalidate", "serverTime": 1})
		if m := next(ctx); m == nil || m["name"] != "reauthenticate" {
			t.Errorf("after revalidate got %v, want reauthenticate", m)
		}
		time.Sleep(50 * time.Millisecond)
		return "server close"
	})

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := New(Config{WSURL: fs.url, Channel: "general",
		Minter: &countingMinter{}, Log: logger})
	go c.Run(ctx)
	for range c.Events() {
	}

	log := buf.String()
	for _, tok := range []string{"tok1", "tok2"} {
		if strings.Contains(log, tok) {
			t.Errorf("log leaks token %q:\n%s", tok, log)
		}
	}
	for _, want := range []string{
		`name=authenticate`, `name=reauthenticate`, `[redacted]`,
		`msg=authenticated`, `msg=subscribed`, `msg=stopped`,
		`name=subscribed`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "name=ping") || strings.Contains(log, "name=pong") {
		t.Errorf("heartbeat frames should be skipped:\n%s", log)
	}
}
