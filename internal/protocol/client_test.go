package protocol

import (
	"encoding/json"
	"testing"
)

// TestClientMessageWireFormat pins the exact JSON of every client→server
// message. The server strict-validates these schemas and rejects unknown or
// missing fields, and the types here are hand-ported with no compile-time
// link to them (ADR 0002) — so byte-for-byte is the assertion that matters.
func TestClientMessageWireFormat(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want string
	}{
		{"authenticate", NewAuthenticate("tok"),
			`{"name":"authenticate","token":"tok"}`},
		{"reauthenticate", NewReauthenticate("tok"),
			`{"name":"reauthenticate","token":"tok"}`},
		{"message", NewChatMessage(42, "hi"),
			`{"name":"message","channelID":42,"message":"hi"}`},
		{"getChannelID", NewGetChannelID("general"),
			`{"name":"getChannelID","channelName":"general"}`},
		{"ping", NewPing(1700000000000),
			`{"name":"ping","data":{"time":1700000000000}}`},
		{"subscribe", NewSubscribe(7),
			`{"name":"subscribe","channelID":7}`},
		{"typing", NewTyping(7),
			`{"name":"typing","channelID":7}`},
		{"unsubscribe", NewUnsubscribe(7),
			`{"name":"unsubscribe","channelID":7}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(raw) != tc.want {
				t.Errorf("\n got %s\nwant %s", raw, tc.want)
			}
		})
	}
}
