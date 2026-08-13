package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/coder/websocket"

	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
)

// msgFrame builds a server message frame with the given ID.
func msgFrame(id int64) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"name":"message","id":%d,"channelID":1,"message":"m%d","username":"bob"}`,
		id, id))
}

// drain collects everything currently buffered on the event channel.
func drain(c *Client) []Event {
	var out []Event
	for {
		select {
		case e := <-c.events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// msgIDs extracts the IDs of message events, in emitted order.
func msgIDs(events []Event) []int64 {
	var ids []int64
	for _, e := range events {
		if m, ok := e.Msg.(protocol.Message); ok && m.ID != nil {
			ids = append(ids, *m.ID)
		}
	}
	return ids
}

func withID(id int64) protocol.Message {
	return protocol.Message{Name: "message", ID: &id}
}

func TestMarkSeenDeduplicates(t *testing.T) {
	c := New(Config{})
	if !c.markSeen(withID(1)) {
		t.Fatal("first sighting should be new")
	}
	if c.markSeen(withID(1)) {
		t.Error("second sighting should be a duplicate")
	}
	if !c.markSeen(withID(2)) {
		t.Error("distinct ID should be new")
	}
}

// TestMarkSeenNilIDAlwaysPasses: without an ID there is nothing to dedupe on,
// so the message must display rather than be dropped as a false duplicate.
func TestMarkSeenNilIDAlwaysPasses(t *testing.T) {
	c := New(Config{})
	m := protocol.Message{Name: "message"}
	if !c.markSeen(m) || !c.markSeen(m) {
		t.Error("nil-ID messages must never be treated as duplicates")
	}
	if c.everSaw {
		t.Error("a nil-ID message should not count as history for gap detection")
	}
}

// TestMarkSeenWindowEvicts bounds memory over a long session; the oldest IDs
// fall out and would be re-shown if the server ever replayed them.
func TestMarkSeenWindowEvicts(t *testing.T) {
	c := New(Config{})
	for id := int64(1); id <= dedupeWindow; id++ {
		c.markSeen(withID(id))
	}
	if c.markSeen(withID(1)) {
		t.Error("ID 1 should still be within the window")
	}
	c.markSeen(withID(dedupeWindow + 1)) // evicts the oldest
	if !c.markSeen(withID(1)) {
		t.Error("ID 1 should have been evicted from the window")
	}
	if len(c.seen) != len(c.seenOrder) {
		t.Errorf("index and order drifted: %d vs %d",
			len(c.seen), len(c.seenOrder))
	}
}

// TestDeliverBackfillReversesOrder: the buffer arrives newest-first, and the
// transcript has to read downward.
func TestDeliverBackfillReversesOrder(t *testing.T) {
	c := New(Config{})
	sub := protocol.Subscribed{ChannelID: 1, MessageBuffer: []json.RawMessage{
		msgFrame(3), msgFrame(2), msgFrame(1),
	}}
	c.deliverBackfill(context.Background(), sub)

	events := drain(c)
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	if _, ok := events[0].Msg.(protocol.Subscribed); !ok {
		t.Errorf("first event is %T, want Subscribed", events[0].Msg)
	}
	got := msgIDs(events)
	want := []int64{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("emitted IDs %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("emitted IDs %v, want %v", got, want)
		}
	}
}

func TestDeliverBackfillGapDetection(t *testing.T) {
	tests := []struct {
		name    string
		seed    []int64 // IDs already displayed before the reconnect
		buffer  []int64
		wantGap bool
		wantNew []int64
	}{
		{"first subscribe is never a gap",
			nil, []int64{3, 2, 1}, false, []int64{1, 2, 3}},
		{"overlap means continuous history",
			[]int64{1, 2}, []int64{3, 2, 1}, false, []int64{3}},
		{"no overlap means history was lost",
			[]int64{100}, []int64{3, 2, 1}, true, []int64{1, 2, 3}},
		{"empty buffer cannot prove a gap",
			[]int64{100}, nil, false, nil},
		{"fully duplicate buffer is not a gap",
			[]int64{1, 2, 3}, []int64{3, 2, 1}, false, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := New(Config{})
			for _, id := range tc.seed {
				c.markSeen(withID(id))
			}
			var buf []json.RawMessage
			for _, id := range tc.buffer {
				buf = append(buf, msgFrame(id))
			}
			c.deliverBackfill(context.Background(),
				protocol.Subscribed{ChannelID: 1, MessageBuffer: buf})

			events := drain(c)
			if events[0].Gap != tc.wantGap {
				t.Errorf("Gap = %v, want %v", events[0].Gap, tc.wantGap)
			}
			got := msgIDs(events)
			if len(got) != len(tc.wantNew) {
				t.Fatalf("replayed %v, want %v", got, tc.wantNew)
			}
			for i := range tc.wantNew {
				if got[i] != tc.wantNew[i] {
					t.Fatalf("replayed %v, want %v", got, tc.wantNew)
				}
			}
		})
	}
}

// TestDeliverBackfillSkipsUndecodable keeps a malformed buffer entry from
// aborting the whole replay.
func TestDeliverBackfillSkipsUndecodable(t *testing.T) {
	c := New(Config{})
	c.deliverBackfill(context.Background(), protocol.Subscribed{
		ChannelID: 1,
		MessageBuffer: []json.RawMessage{
			msgFrame(2),
			json.RawMessage(`{"name":"userJoined","username":"bob"}`),
			json.RawMessage(`garbage`),
			msgFrame(1),
		},
	})
	if got := msgIDs(drain(c)); len(got) != 2 {
		t.Errorf("replayed %v, want the 2 decodable messages", got)
	}
}

func TestCloseReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"close error", websocket.CloseError{
			Code: websocket.StatusNormalClosure, Reason: "idle timeout",
		}, "idle timeout"},
		{"wrapped close error", fmt.Errorf("read: %w", websocket.CloseError{
			Code: websocket.StatusNormalClosure, Reason: "server close",
		}), "server close"},
		{"plain error", errors.New("connection reset"), ""},
		{"nil error", nil, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := closeReason(tc.err); got != tc.want {
				t.Errorf("closeReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNoReconnectReasons pins the close reasons that must end the session
// rather than trigger the reconnect loop.
func TestNoReconnectReasons(t *testing.T) {
	for _, reason := range []string{"client close", "server close",
		"idle timeout"} {
		if !noReconnectReasons[reason] {
			t.Errorf("%q should suppress reconnect", reason)
		}
	}
	for _, reason := range []string{"", "going away", "abnormal closure"} {
		if noReconnectReasons[reason] {
			t.Errorf("%q should allow reconnect", reason)
		}
	}
}

// TestSendChatRequiresChannel: writes before subscribe must fail loudly
// rather than silently dropping the user's line.
func TestSendChatRequiresChannel(t *testing.T) {
	c := New(Config{})
	if err := c.SendChat("hi"); err == nil {
		t.Error("SendChat should fail before a channel is joined")
	}
	c.SendTyping() // must not panic when disconnected
}
