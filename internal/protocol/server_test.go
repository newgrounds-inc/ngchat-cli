package protocol

import (
	"testing"
)

func TestDecodeKnownNames(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		check func(*testing.T, any)
	}{
		{"authenticated",
			`{"name":"authenticated","username":"bob","userID":7,"isChatMod":true}`,
			func(t *testing.T, v any) {
				a, ok := v.(Authenticated)
				if !ok {
					t.Fatalf("got %T, want Authenticated", v)
				}
				if a.Username != "bob" || a.UserID != 7 || !a.IsChatMod {
					t.Errorf("bad decode: %+v", a)
				}
			}},
		{"unauthorized",
			`{"name":"unauthorized","message":"jwt expired"}`,
			func(t *testing.T, v any) {
				u, ok := v.(Unauthorized)
				if !ok || u.Message != "jwt expired" || u.ChannelID != 0 {
					t.Errorf("got %#v", v)
				}
			}},
		{"unauthorized on subscribe",
			`{"name":"unauthorized","channelID":3,"message":"You have been banned from this channel."}`,
			func(t *testing.T, v any) {
				u, ok := v.(Unauthorized)
				if !ok || u.ChannelID != 3 {
					t.Errorf("got %#v", v)
				}
			}},
		{"error with redirect",
			`{"name":"error","message":"no such channel","redirectChannel":"general"}`,
			func(t *testing.T, v any) {
				e, ok := v.(Error)
				if !ok || e.RedirectChannel != "general" {
					t.Errorf("got %#v", v)
				}
			}},
		{"channelID",
			`{"name":"channelID","channelID":3,"channelName":"general"}`,
			func(t *testing.T, v any) {
				c, ok := v.(ChannelID)
				if !ok || c.ChannelID != 3 || c.ChannelName != "general" {
					t.Errorf("got %#v", v)
				}
			}},
		{"subscribed",
			`{"name":"subscribed","channelID":3,"channelName":"general","messageBuffer":[{"name":"message"},{"name":"message"}],` +
				`"userList":[{"userID":7,"username":"bob","isChatMod":true,"isAway":true,"awayMessage":"<b>brb</b>","awayMessageRaw":"brb"}],` +
				`"notifications":[{"message":"hi <b>you</b>","messageRaw":"hi you","messageType":"directMessage","serverTime":5,"userID":8,"username":"ann"}]}`,
			func(t *testing.T, v any) {
				s, ok := v.(Subscribed)
				if !ok || s.ChannelID != 3 || len(s.MessageBuffer) != 2 {
					t.Fatalf("got %#v", v)
				}
				if len(s.UserList) != 1 || s.UserList[0].Username != "bob" ||
					!s.UserList[0].IsChatMod || !s.UserList[0].IsAway ||
					s.UserList[0].AwayMessage != "<b>brb</b>" {
					t.Errorf("userList = %#v", s.UserList)
				}
				if len(s.Notifications) != 1 ||
					s.Notifications[0].MessageType != "directMessage" ||
					s.Notifications[0].Username != "ann" ||
					s.Notifications[0].ServerTime != 5 {
					t.Errorf("notifications = %#v", s.Notifications)
				}
			}},
		// The legacy shim sends the roster with no users and a DB-backed
		// notification whose user columns are null; neither may turn the
		// whole subscribe into Unknown.
		{"subscribed with null user columns",
			`{"name":"subscribed","channelID":3,"messageBuffer":[],"userList":[],"notifications":[{"message":"m","messageRaw":"m","messageType":"message","serverTime":1,"userID":null,"username":null}]}`,
			func(t *testing.T, v any) {
				s, ok := v.(Subscribed)
				if !ok || len(s.Notifications) != 1 || s.Notifications[0].Username != "" {
					t.Errorf("got %#v", v)
				}
			}},
		{"away",
			`{"name":"away","channelID":3,"userID":7,"username":"bob","isAway":true,"awayMessage":"<i>lunch</i>","awayMessageRaw":"lunch","isAdmin":false,"isChatMod":false,"serverTime":9}`,
			func(t *testing.T, v any) {
				a, ok := v.(Away)
				if !ok || a.Username != "bob" || !a.IsAway ||
					a.AwayMessage != "<i>lunch</i>" || a.UserID != 7 {
					t.Errorf("got %#v", v)
				}
			}},
		{"away back without message fields",
			`{"name":"away","channelID":3,"userID":7,"username":"bob","isAway":false,"isAdmin":false,"isChatMod":false,"serverTime":9}`,
			func(t *testing.T, v any) {
				a, ok := v.(Away)
				if !ok || a.IsAway || a.AwayMessage != "" {
					t.Errorf("got %#v", v)
				}
			}},
		{"unsubscribed",
			`{"name":"unsubscribed","channelID":3}`,
			func(t *testing.T, v any) {
				if _, ok := v.(Unsubscribed); !ok {
					t.Errorf("got %T", v)
				}
			}},
		{"typing",
			`{"name":"typing","channelID":3,"userID":7,"username":"bob"}`,
			func(t *testing.T, v any) {
				e, ok := v.(TypingEvent)
				if !ok || e.Username != "bob" {
					t.Errorf("got %#v", v)
				}
			}},
		{"userJoined",
			`{"name":"userJoined","channelID":3,"username":"bob"}`,
			func(t *testing.T, v any) {
				if _, ok := v.(UserJoined); !ok {
					t.Errorf("got %T", v)
				}
			}},
		{"userLeft",
			`{"name":"userLeft","channelID":3,"username":"bob"}`,
			func(t *testing.T, v any) {
				if _, ok := v.(UserLeft); !ok {
					t.Errorf("got %T", v)
				}
			}},
		{"pong",
			`{"name":"pong","data":{"time":123}}`,
			func(t *testing.T, v any) {
				p, ok := v.(Pong)
				if !ok || p.Data.Time != 123 {
					t.Errorf("got %#v", v)
				}
			}},
		{"userUpdated",
			`{"name":"userUpdated","channelID":3,"username":"bob","isChatMod":true,"isAway":true,"awayMessage":"brb","awayMessageRaw":"brb","userIcon":"i.png","userPageURL":"https://bob.newgrounds.com"}`,
			func(t *testing.T, v any) {
				u, ok := v.(UserUpdated)
				if !ok || u.Username != "bob" || !u.IsChatMod || !u.IsAway ||
					u.AwayMessage != "brb" || u.AwayMessageRaw != "brb" ||
					u.UserIcon != "i.png" ||
					u.UserPageURL != "https://bob.newgrounds.com" {
					t.Errorf("got %#v", v)
				}
			}},
		{"kicked with reason",
			`{"name":"kicked","reason":"bye","kickedByUserID":5,"channelID":3}`,
			func(t *testing.T, v any) {
				k, ok := v.(Kicked)
				if !ok || k.Reason != "bye" || k.KickedByUserID != 5 {
					t.Errorf("got %#v", v)
				}
			}},
		{"kicked without reason",
			`{"name":"kicked","serverTime":1}`,
			func(t *testing.T, v any) {
				k, ok := v.(Kicked)
				if !ok || k.Reason != "" {
					t.Errorf("got %#v", v)
				}
			}},
		{"idleTimeout",
			`{"name":"idleTimeout","reason":"no activity for 24h"}`,
			func(t *testing.T, v any) {
				i, ok := v.(IdleTimeout)
				if !ok || i.Reason != "no activity for 24h" {
					t.Errorf("got %#v", v)
				}
			}},
		{"revalidate",
			`{"name":"revalidate","serverTime":1700000000000}`,
			func(t *testing.T, v any) {
				r, ok := v.(Revalidate)
				if !ok || r.ServerTime != 1700000000000 {
					t.Errorf("got %#v", v)
				}
			}},
		{"revalidated",
			`{"name":"revalidated","isAdmin":false,"isChatMod":true,"isSiteMod":false,"serverTime":1}`,
			func(t *testing.T, v any) {
				r, ok := v.(Revalidated)
				if !ok || r.IsAdmin || !r.IsChatMod {
					t.Errorf("got %#v", v)
				}
			}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, Decode([]byte(tc.frame)))
		})
	}
}

// TestDecodeMessageVariants covers the four names that share Message's
// shape; the UI tells them apart by Name alone.
func TestDecodeMessageVariants(t *testing.T) {
	for _, name := range []string{"message", "meMessage", "slapMessage",
		"serverMessage", "directMessage"} {
		t.Run(name, func(t *testing.T) {
			frame := `{"name":"` + name +
				`","id":99,"channelID":3,"message":"<b>hi</b>",` +
				`"messageRaw":"**hi**","username":"bob","isSpoiler":true}`
			msg, ok := Decode([]byte(frame)).(Message)
			if !ok {
				t.Fatalf("did not decode as Message")
			}
			if msg.Name != name {
				t.Errorf("Name = %q, want %q", msg.Name, name)
			}
			if msg.ID == nil || *msg.ID != 99 {
				t.Errorf("ID = %v, want 99", msg.ID)
			}
			if !msg.IsSpoiler || msg.Message != "<b>hi</b>" {
				t.Errorf("bad decode: %+v", msg)
			}
		})
	}
}

// TestDecodeMissingIDIsNil matters because a nil ID opts a message out of
// dedupe rather than colliding on a zero value.
func TestDecodeMissingIDIsNil(t *testing.T) {
	msg, ok := Decode([]byte(`{"name":"message","message":"hi"}`)).(Message)
	if !ok {
		t.Fatalf("did not decode as Message")
	}
	if msg.ID != nil {
		t.Errorf("ID = %v, want nil", *msg.ID)
	}
}

// TestDecodeIsTolerant is the drift policy from ADR 0002: nothing the server
// sends may produce an error, so old binaries survive protocol additions.
func TestDecodeTolerance(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		want  string // expected Unknown.Name
	}{
		{"unrecognized name", `{"name":"somethingNew","x":1}`, "somethingNew"},
		{"known name, undecodable payload",
			`{"name":"channelID","channelID":"not-a-number"}`, "channelID"},
		{"invalid json", `not json at all`, ""},
		{"empty frame", ``, ""},
		{"json but not an object", `[1,2,3]`, ""},
		{"missing name", `{"channelID":3}`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Decode([]byte(tc.frame)).(Unknown)
			if !ok {
				t.Fatalf("got %T, want Unknown", Decode([]byte(tc.frame)))
			}
			if got.Name != tc.want {
				t.Errorf("Unknown.Name = %q, want %q", got.Name, tc.want)
			}
		})
	}
}

// TestDecodeUnknownNameIsNotDroppedSilently documents that an added server
// field on a known frame still decodes — Go ignores unknown JSON fields, and
// that asymmetry with the strict client schemas is intentional.
func TestDecodeIgnoresNewServerFields(t *testing.T) {
	v := Decode([]byte(
		`{"name":"channelID","channelID":3,"channelName":"g","futureField":true}`))
	c, ok := v.(ChannelID)
	if !ok || c.ChannelID != 3 {
		t.Errorf("got %#v, want ChannelID{3,...}", v)
	}
}

func TestMOTDText(t *testing.T) {
	tests := []struct {
		name  string
		frame string
		want  string
	}{
		{"string motd",
			`{"name":"authenticated","motd":"welcome"}`, "welcome"},
		{"object motd", `{"name":"authenticated","motd":{"text":"x"}}`, ""},
		{"null motd", `{"name":"authenticated","motd":null}`, ""},
		{"absent motd", `{"name":"authenticated"}`, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, ok := Decode([]byte(tc.frame)).(Authenticated)
			if !ok {
				t.Fatalf("did not decode as Authenticated")
			}
			if got := a.MOTDText(); got != tc.want {
				t.Errorf("MOTDText() = %q, want %q", got, tc.want)
			}
		})
	}
}
