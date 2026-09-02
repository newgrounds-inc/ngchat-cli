// Package client maintains the NG Chat WebSocket session: mint → dial →
// authenticate → resolve channel → subscribe, plus the heartbeat the
// server requires and the reconnect/dedupe logic that makes the hourly
// JWT-expiry disconnect invisible to the UI.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
)

// State describes the connection lifecycle for the UI.
type State int

// Connection states.
const (
	StateConnecting State = iota
	StateOnline
	StateReconnecting
	StateStopped
)

// Event is what the UI consumes: a decoded protocol message, a state
// change, or a detected history gap after reconnecting.
type Event struct {
	// Msg is a decoded protocol value (protocol.Message etc.) or nil
	// for pure state changes.
	Msg any
	// State is the connection state as of this event.
	State State
	// Gap is true when a reconnect's backfill did not overlap
	// already-seen messages — history was lost and cannot be fetched.
	Gap bool
	// Err carries the terminal error when State is StateStopped, or the
	// reason the last session ended when State is StateReconnecting.
	Err error
}

// RenewalFailed is emitted as Event.Msg when the client could not answer
// a server Revalidate (the re-mint failed). It is informational: the
// server's close timer is still armed, and the ordinary reconnect path
// takes over at expiry.
type RenewalFailed struct {
	Err error
}

// Config wires a Client.
type Config struct {
	WSURL   string
	Channel string
	Minter  auth.Minter
	// Cookie is an extra Cookie header sent on the WS upgrade (dev
	// proxy routing).
	Cookie string
}

const (
	// heartbeatEvery keeps well inside the server's 15s inbound-traffic
	// deadline.
	heartbeatEvery = 5 * time.Second
	// readTimeout doubles as the dead-connection watchdog: pongs arrive
	// every heartbeat, so a silent 20s means the link is gone.
	readTimeout    = 20 * time.Second
	maxAuthRetries = 3
	maxBackoff     = 30 * time.Second
	// renewalMintTimeout bounds the re-mint done inline in the read loop
	// on Revalidate. The heartbeat goroutine keeps the socket alive
	// meanwhile, but inbound frames queue until the mint returns, so
	// this stays well under the two-minute window the server allows.
	renewalMintTimeout = 15 * time.Second
	// dedupeWindow bounds the remembered message IDs used to collapse
	// reconnect backfill against what was already displayed.
	dedupeWindow = 500
)

// Client runs one chat session with automatic reconnect.
type Client struct {
	cfg    Config
	events chan Event

	mu        sync.Mutex
	conn      *websocket.Conn
	channelID int

	seen      map[int64]struct{}
	seenOrder []int64
	everSaw   bool
}

// New builds a Client; call Run to start it.
func New(cfg Config) *Client {
	return &Client{
		cfg:    cfg,
		events: make(chan Event, 64),
		seen:   make(map[int64]struct{}),
	}
}

// Events returns the stream the UI should drain. It is closed when Run
// returns.
func (c *Client) Events() <-chan Event {
	return c.events
}

// Run connects and keeps the session alive until ctx is canceled or the
// server sends a do-not-reconnect close ("client close", "server close",
// "idle timeout") or a hard auth rejection.
func (c *Client) Run(ctx context.Context) {
	defer close(c.events)
	backoff := time.Second
	for {
		established, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		var stop *stopError
		if errors.As(err, &stop) {
			c.emit(ctx, Event{State: StateStopped, Err: err})
			return
		}
		if established {
			backoff = time.Second
		}
		c.emit(ctx, Event{State: StateReconnecting, Err: err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// stopError marks conditions that must not trigger auto-reconnect.
type stopError struct{ reason string }

func (e *stopError) Error() string { return e.reason }

// noReconnectReasons are close reasons the browser client also treats as
// final.
var noReconnectReasons = map[string]bool{
	"client close": true,
	"server close": true,
	"idle timeout": true,
}

// session runs one connect-to-disconnect cycle. established reports
// whether it got as far as a subscription (used to reset backoff).
func (c *Client) session(ctx context.Context) (established bool, err error) {
	c.emit(ctx, Event{State: StateConnecting})

	token, err := c.cfg.Minter.Mint(ctx)
	if err != nil {
		return false, fmt.Errorf("minting chat JWT: %w", err)
	}

	opts := &websocket.DialOptions{}
	if c.cfg.Cookie != "" {
		opts.HTTPHeader = http.Header{"Cookie": {c.cfg.Cookie}}
	}
	conn, _, err := websocket.Dial(ctx, c.cfg.WSURL, opts)
	if err != nil {
		return false, fmt.Errorf("dialing %s: %w", c.cfg.WSURL, err)
	}
	c.mu.Lock()
	c.conn = conn
	c.channelID = 0
	c.mu.Unlock()
	defer func() {
		conn.Close(websocket.StatusNormalClosure, "")
		c.mu.Lock()
		c.conn = nil
		c.channelID = 0
		c.mu.Unlock()
	}()

	if err := c.send(ctx, protocol.NewAuthenticate(token)); err != nil {
		return false, err
	}

	sessionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go c.heartbeat(sessionCtx)

	authRetries := 0
	idleReason := ""
	for {
		readCtx, cancelRead := context.WithTimeout(ctx, readTimeout)
		_, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			if reason := closeReason(err); noReconnectReasons[reason] {
				if reason == "idle timeout" && idleReason != "" {
					reason += ": " + idleReason
				}
				return established, &stopError{reason: reason}
			}
			return established, err
		}

		switch msg := protocol.Decode(data).(type) {
		case protocol.Authenticated:
			c.emit(ctx, Event{Msg: msg, State: StateOnline})
			name := strings.ToLower(c.cfg.Channel)
			if err := c.send(ctx, protocol.NewGetChannelID(name)); err != nil {
				return established, err
			}
		case protocol.ChannelID:
			c.mu.Lock()
			c.channelID = msg.ChannelID
			c.mu.Unlock()
			if err := c.send(ctx, protocol.NewSubscribe(msg.ChannelID)); err != nil {
				return established, err
			}
		case protocol.Subscribed:
			established = true
			c.deliverBackfill(ctx, msg)
		case protocol.Unauthorized:
			soft := strings.Contains(msg.Message, "expired") ||
				strings.Contains(msg.Message, "malformed")
			if !soft || authRetries >= maxAuthRetries {
				return established, &stopError{
					reason: "unauthorized: " + msg.Message,
				}
			}
			authRetries++
			token, err := c.cfg.Minter.Mint(ctx)
			if err != nil {
				return established, fmt.Errorf("re-minting chat JWT: %w", err)
			}
			if err := c.send(ctx, protocol.NewAuthenticate(token)); err != nil {
				return established, err
			}
		case protocol.Message:
			if c.markSeen(msg) {
				c.emit(ctx, Event{Msg: msg, State: StateOnline})
			}
		case protocol.Kicked:
			reason := "kicked"
			if msg.Reason != "" {
				reason += ": " + msg.Reason
			}
			return established, &stopError{reason: reason}
		case protocol.IdleTimeout:
			// The close frame that follows carries only "idle timeout";
			// keep the server's explanation for the stop error.
			idleReason = msg.Reason
		case protocol.Revalidate:
			// Exactly one attempt per nudge. The server caps attempts per
			// token and the site's mint limiter counts before auth, so a
			// retry loop here would also lock the user's browser out.
			mintCtx, cancelMint := context.WithTimeout(ctx, renewalMintTimeout)
			token, err := c.cfg.Minter.Mint(mintCtx)
			cancelMint()
			if err != nil {
				c.emit(ctx, Event{
					Msg:   RenewalFailed{Err: err},
					State: StateOnline,
				})
				continue
			}
			if err := c.send(ctx, protocol.NewReauthenticate(token)); err != nil {
				return established, err
			}
		case protocol.Pong, protocol.Unknown:
			// Heartbeat replies and unrecognized frames are dropped;
			// tolerating the latter is the drift policy (ADR 0002).
		default:
			c.emit(ctx, Event{Msg: msg, State: StateOnline})
		}
	}
}

// deliverBackfill replays the subscribe buffer through dedupe, flagging a
// gap when nothing overlaps what was already displayed.
func (c *Client) deliverBackfill(ctx context.Context, sub protocol.Subscribed) {
	overlap := false
	var fresh []protocol.Message
	for _, raw := range sub.MessageBuffer {
		msg, ok := protocol.Decode([]byte(raw)).(protocol.Message)
		if !ok || msg.ID == nil {
			continue
		}
		if _, dup := c.seen[*msg.ID]; dup {
			overlap = true
			continue
		}
		fresh = append(fresh, msg)
	}
	gap := c.everSaw && !overlap && len(fresh) > 0
	c.emit(ctx, Event{Msg: sub, State: StateOnline, Gap: gap})
	// The buffer arrives newest-first; replay oldest-first so the
	// transcript reads downward.
	for i, j := 0, len(fresh)-1; i < j; i, j = i+1, j-1 {
		fresh[i], fresh[j] = fresh[j], fresh[i]
	}
	for _, msg := range fresh {
		if c.markSeen(msg) {
			c.emit(ctx, Event{Msg: msg, State: StateOnline})
		}
	}
}

// markSeen records a message ID, reporting false for duplicates (the
// hourly-reconnect backfill overlaps live traffic already shown).
func (c *Client) markSeen(msg protocol.Message) bool {
	if msg.ID == nil {
		return true
	}
	if _, dup := c.seen[*msg.ID]; dup {
		return false
	}
	c.seen[*msg.ID] = struct{}{}
	c.seenOrder = append(c.seenOrder, *msg.ID)
	if len(c.seenOrder) > dedupeWindow {
		delete(c.seen, c.seenOrder[0])
		c.seenOrder = c.seenOrder[1:]
	}
	c.everSaw = true
	return true
}

// heartbeat pings for the life of one connection; the server drops any
// socket silent for 15s.
func (c *Client) heartbeat(ctx context.Context) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = c.send(ctx, protocol.NewPing(time.Now().UnixMilli()))
		}
	}
}

// SendChat sends a chat line (or slash command) to the joined channel.
func (c *Client) SendChat(text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.channelID == 0 {
		return errors.New("not connected to a channel yet")
	}
	return c.writeLocked(context.Background(),
		protocol.NewChatMessage(c.channelID, text))
}

// SendTyping signals composing; errors are ignorable.
func (c *Client) SendTyping() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil || c.channelID == 0 {
		return
	}
	_ = c.writeLocked(context.Background(),
		protocol.NewTyping(c.channelID))
}

// send marshals and writes one frame.
func (c *Client) send(ctx context.Context, v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeLocked(ctx, v)
}

// writeLocked requires c.mu held; serializes writers over the socket.
func (c *Client) writeLocked(ctx context.Context, v any) error {
	if c.conn == nil {
		return errors.New("not connected")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, data)
}

// emit delivers an event unless the consumer is gone.
func (c *Client) emit(ctx context.Context, e Event) {
	select {
	case c.events <- e:
	case <-ctx.Done():
	}
}

// closeReason extracts the server's close reason, if this was a close
// frame.
func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}
