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
	"log/slog"
	"net/http"
	"regexp"
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
	// Backfill marks a Msg replayed from the subscribe buffer rather than
	// received live, so the UI can show it without treating it as news
	// (no bell for an old mention on every reconnect).
	Backfill bool
	// Err carries the terminal error when State is StateStopped, or the
	// reason the last session ended when State is StateReconnecting.
	Err error
}

// BackfillDone is emitted as Event.Msg once every message from a
// subscribe's backfill buffer has been replayed, so the UI can place
// anything that belongs after the history (the MOTD) at the bottom.
type BackfillDone struct{}

// AccessDenied is the cause of a stop when the server refuses the first
// authenticate outright (not a supporter, under 18, e-mail not validated,
// banned from the server) or refuses the subscribe (banned from the
// channel, alt of a banned account). The site minted the token, so the
// account is not signed out; nothing in the client can change the
// answer, so it is final. Message is server-authored HTML (the supporter
// notice carries a link), so a consumer renders it as markup.
type AccessDenied struct {
	Message string
}

func (e *AccessDenied) Error() string { return "access denied" }

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
	// Log, when set, receives every frame (see Redact), state transition
	// and mint outcome. Heartbeat pings and pongs are skipped so an hour
	// of log stays readable. nil disables logging.
	Log *slog.Logger
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
	// mintTimeout bounds every re-mint done on the read goroutine. While
	// a mint is in flight nothing reads the socket, so the 20s read
	// timeout that doubles as the dead-link watchdog is suspended for
	// this long; keeping it well under readTimeout keeps that window
	// honest. The heartbeat goroutine still feeds the server's 15s
	// silence deadline meanwhile.
	mintTimeout = 8 * time.Second
	// Renewal budget. The site's mint limiter is 10/min per user and
	// counts before auth, so a client answering nudges in a loop locks
	// the user's browser out of chat too. An acked renewal may be
	// followed immediately (a short-TTL dev token nudges right after
	// every ack); an unacked one waits minRenewalSpacing before another
	// try; and maxRenewalsPerMinute caps both, matching the server's own
	// per-token attempt limit.
	minRenewalSpacing    = 10 * time.Second
	maxRenewalsPerMinute = 5
	// dedupeWindow bounds the remembered message IDs used to collapse
	// reconnect backfill against what was already displayed.
	dedupeWindow = 500
)

// Client runs one chat session with automatic reconnect.
type Client struct {
	cfg    Config
	events chan Event
	log    *slog.Logger

	mu        sync.Mutex
	conn      *websocket.Conn
	channelID int

	seen      map[int64]struct{}
	seenOrder []int64
	everSaw   bool
}

// New builds a Client; call Run to start it.
func New(cfg Config) *Client {
	c := &Client{
		cfg:    cfg,
		events: make(chan Event, 64),
		seen:   make(map[int64]struct{}),
		log:    cfg.Log,
	}
	if c.log == nil {
		c.log = slog.New(slog.DiscardHandler)
	}
	return c
}

var (
	tokenField = regexp.MustCompile(`("token"\s*:\s*")[^"]*(")`)
	cookiePair = regexp.MustCompile(
		`\b(ng_remember|newgrounds_session|XSRF-TOKEN|serverid)=[^;\s"]*`)
)

// logFrameLimit bounds one logged frame; a 5000-character message with
// HTML is still recognizable at this size.
const logFrameLimit = 1000

// Redact masks the values that must never reach a log: chat JWTs in
// authenticate and reauthenticate frames and the site's cookies in any
// header or error text. Everything else passes through unchanged.
func Redact(s string) string {
	s = tokenField.ReplaceAllString(s, "${1}[redacted]${2}")
	return cookiePair.ReplaceAllString(s, "${1}=[redacted]")
}

// logFrame writes one wire frame to the debug log, redacted and bounded.
func (c *Client) logFrame(dir string, data []byte) {
	if c.cfg.Log == nil {
		return
	}
	name := protocol.FrameName(data)
	if name == "ping" || name == "pong" {
		return
	}
	frame := Redact(string(data))
	if len(frame) > logFrameLimit {
		frame = frame[:logFrameLimit] + "…"
	}
	c.log.Debug("frame", "dir", dir, "name", name, "frame", frame)
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
			c.log.Info("stopped", "reason", Redact(err.Error()))
			c.emit(ctx, Event{State: StateStopped, Err: err})
			return
		}
		if established {
			backoff = time.Second
		}
		c.log.Info("reconnecting", "after", backoff,
			"reason", Redact(fmt.Sprint(err)))
		c.emit(ctx, Event{State: StateReconnecting, Err: err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// stopError marks conditions that must not trigger auto-reconnect. err,
// when set, is the cause so callers can errors.Is it (auth.ErrSignedOut).
type stopError struct {
	reason string
	err    error
}

func (e *stopError) Error() string { return e.reason }
func (e *stopError) Unwrap() error { return e.err }

// mintFailure classifies a failed mint. A 401 from the site means the
// remember cookie is gone (password changed, or `ngchat logout` ran):
// no reconnect can fix it, so it is final and the UI tells the user to
// log in again. Anything else is transient and reconnects with backoff.
func mintFailure(stage string, err error) error {
	if errors.Is(err, auth.ErrSignedOut) {
		return &stopError{reason: "signed out", err: err}
	}
	return fmt.Errorf("%s: %w", stage, err)
}

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
	c.log.Info("connecting", "url", c.cfg.WSURL)

	mintCtx, cancelMint := context.WithTimeout(ctx, mintTimeout)
	token, err := c.cfg.Minter.Mint(mintCtx)
	cancelMint()
	if err != nil {
		c.log.Info("mint failed", "stage", "connect", "err", Redact(err.Error()))
		return false, mintFailure("minting chat JWT", err)
	}
	c.log.Info("mint ok", "stage", "connect")

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
	authed := false
	renewalPending := false
	var lastMint time.Time
	var renewals []time.Time // mint times inside the last minute
	for {
		readCtx, cancelRead := context.WithTimeout(ctx, readTimeout)
		_, data, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			c.log.Info("read ended", "close_reason", closeReason(err),
				"err", Redact(err.Error()))
			if reason := closeReason(err); noReconnectReasons[reason] {
				return established, &stopError{reason: reason}
			}
			return established, err
		}
		c.logFrame("in", data)

		switch msg := protocol.Decode(data).(type) {
		case protocol.Authenticated:
			authed = true
			c.log.Info("authenticated", "username", msg.Username,
				"isChatMod", msg.IsChatMod, "isAdmin", msg.IsAdmin)
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
			c.log.Info("subscribed", "channelID", msg.ChannelID,
				"backfill", len(msg.MessageBuffer), "users", len(msg.UserList))
			c.deliverBackfill(ctx, msg)
		case protocol.Unauthorized:
			if authed && msg.ChannelID != 0 {
				// The subscribe was refused (channel ban, alt of a banned
				// account). The socket stays open but there is one
				// channel, so an open socket with nothing to join is a
				// stop; the server's message is the whole answer.
				return established, &stopError{
					reason: "access denied",
					err:    &AccessDenied{Message: msg.Message},
				}
			}
			if authed {
				// After authentication this only precedes a server close:
				// token expiry (reason "token expired", which reconnects)
				// or a rejected reauthenticate. Re-sending authenticate
				// would trip the already-authenticated guard, so let the
				// close arrive and decide.
				c.emit(ctx, Event{Msg: msg, State: StateOnline})
				continue
			}
			soft := strings.Contains(msg.Message, "expired") ||
				strings.Contains(msg.Message, "malformed")
			if !soft {
				return established, &stopError{
					reason: "access denied",
					err:    &AccessDenied{Message: msg.Message},
				}
			}
			if authRetries >= maxAuthRetries {
				return established, &stopError{
					reason: "unauthorized: " + msg.Message,
				}
			}
			authRetries++
			c.log.Info("soft unauthorized, re-minting", "attempt", authRetries,
				"message", msg.Message)
			retryCtx, cancelRetry := context.WithTimeout(ctx, mintTimeout)
			retried, mintErr := c.cfg.Minter.Mint(retryCtx)
			cancelRetry()
			if mintErr != nil {
				c.log.Info("mint failed", "stage", "auth retry",
					"err", Redact(mintErr.Error()))
				return established, mintFailure("re-minting chat JWT", mintErr)
			}
			token = retried
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
			// Final on the frame itself: the close that follows may not
			// arrive cleanly, and reconnecting would only idle out again.
			reason := "idle timeout"
			if msg.Reason != "" {
				reason += ": " + msg.Reason
			}
			return established, &stopError{reason: reason}
		case protocol.Revalidate:
			now := time.Now()
			for len(renewals) > 0 && now.Sub(renewals[0]) > time.Minute {
				renewals = renewals[1:]
			}
			if len(renewals) >= maxRenewalsPerMinute ||
				(renewalPending && now.Sub(lastMint) < minRenewalSpacing) {
				c.log.Info("revalidate ignored: renewal budget",
					"renewals_last_minute", len(renewals),
					"pending", renewalPending)
				continue
			}
			lastMint = now
			renewals = append(renewals, now)
			renewCtx, cancelRenew := context.WithTimeout(ctx, mintTimeout)
			fresh, mintErr := c.cfg.Minter.Mint(renewCtx)
			cancelRenew()
			if mintErr == nil && fresh == token {
				// The server closes the socket on a token whose expiry is
				// not strictly later than the one in force, so a cached
				// mint is worse than no answer at all.
				mintErr = errors.New("mint returned the token already in force")
			}
			if errors.Is(mintErr, auth.ErrSignedOut) {
				// The token in force would carry the socket to its
				// deadline, but the account is signed out: stop now
				// rather than pretend for up to two minutes.
				return established, mintFailure("renewing chat JWT", mintErr)
			}
			if mintErr != nil {
				c.log.Info("mint failed", "stage", "renewal",
					"err", Redact(mintErr.Error()))
				c.emit(ctx, Event{
					Msg:   RenewalFailed{Err: mintErr},
					State: StateOnline,
				})
				continue
			}
			c.log.Info("mint ok", "stage", "renewal")
			if err := c.send(ctx, protocol.NewReauthenticate(fresh)); err != nil {
				return established, err
			}
			token = fresh
			renewalPending = true
		case protocol.Revalidated:
			renewalPending = false
			c.log.Info("revalidated", "isChatMod", msg.IsChatMod,
				"isAdmin", msg.IsAdmin)
			c.emit(ctx, Event{Msg: msg, State: StateOnline})
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
			c.emit(ctx, Event{Msg: msg, State: StateOnline, Backfill: true})
		}
	}
	c.emit(ctx, Event{Msg: BackfillDone{}, State: StateOnline})
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
	c.logFrame("out", data)
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
