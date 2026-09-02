package protocol

import "encoding/json"

// envelope is the discriminator every server frame carries.
type envelope struct {
	Name string `json:"name"`
}

// Authenticated confirms a successful authenticate and describes the user.
type Authenticated struct {
	ClientHash string          `json:"clientHash"`
	IsAdmin    bool            `json:"isAdmin"`
	IsChatMod  bool            `json:"isChatMod"`
	IsSiteMod  bool            `json:"isSiteMod"`
	MOTD       json.RawMessage `json:"motd"`
	ServerTime int64           `json:"serverTime"`
	UserID     int             `json:"userID"`
	Username   string          `json:"username"`
}

// MOTDText returns the message of the day when it is a plain string,
// otherwise "".
func (a Authenticated) MOTDText() string {
	var s string
	if err := json.Unmarshal(a.MOTD, &s); err != nil {
		return ""
	}
	return s
}

// Unauthorized reports an auth failure. "jwt expired"/"jwt malformed" are
// soft failures: re-mint and re-send Authenticate on the same socket.
type Unauthorized struct {
	Message string `json:"message"`
}

// Error is a generic request failure; RedirectChannel is set when a
// channel lookup failed and the server suggests another channel.
type Error struct {
	Message         string `json:"message"`
	RedirectChannel string `json:"redirectChannel"`
}

// ChannelID answers GetChannelID.
type ChannelID struct {
	ChannelID   int    `json:"channelID"`
	ChannelName string `json:"channelName"`
}

// Subscribed answers Subscribe. MessageBuffer holds up to 25 recent
// events (same envelopes as live ones — decode each with Decode); it is
// the only history a client ever receives.
type Subscribed struct {
	ChannelID     int               `json:"channelID"`
	MessageBuffer []json.RawMessage `json:"messageBuffer"`
	UserList      json.RawMessage   `json:"userList"`
	Notifications json.RawMessage   `json:"notifications"`
	ServerTime    int64             `json:"serverTime"`
}

// Unsubscribed answers Unsubscribe.
type Unsubscribed struct {
	ChannelID int `json:"channelID"`
}

// Message is a chat line. It also covers the meMessage, slapMessage,
// serverMessage and directMessage variants, which share its shape; check
// Name to tell them apart. Message is server-rendered HTML; MessageRaw is
// the sender's original text.
type Message struct {
	Name        string   `json:"name"`
	ChannelID   int      `json:"channelID"`
	ID          *int64   `json:"id"`
	Message     string   `json:"message"`
	MessageRaw  string   `json:"messageRaw"`
	Mentions    []string `json:"mentions"`
	IsSpoiler   bool     `json:"isSpoiler"`
	IsAdmin     bool     `json:"isAdmin"`
	IsChatMod   bool     `json:"isChatMod"`
	UserIcon    string   `json:"userIcon"`
	UserID      int      `json:"userID"`
	Username    string   `json:"username"`
	UserPageURL string   `json:"userPageURL"`
	ServerTime  int64    `json:"serverTime"`
}

// TypingEvent reports another user composing in a channel.
type TypingEvent struct {
	ChannelID int    `json:"channelID"`
	UserID    int    `json:"userID"`
	Username  string `json:"username"`
}

// UserJoined reports a user entering a channel.
type UserJoined struct {
	ChannelID   int    `json:"channelID"`
	UserID      int    `json:"userID"`
	Username    string `json:"username"`
	IsAdmin     bool   `json:"isAdmin"`
	IsChatMod   bool   `json:"isChatMod"`
	IsAway      bool   `json:"isAway"`
	AwayMessage string `json:"awayMessage"`
	ServerTime  int64  `json:"serverTime"`
}

// UserUpdated is a silent roster patch: the same shape as UserJoined but
// for a user already in the channel (a mid-session role or away change,
// typically published by an in-place token renewal). It must not be
// announced as a join, and an unknown username is ignored rather than
// added.
type UserUpdated UserJoined

// UserLeft reports a user leaving a channel.
type UserLeft struct {
	ChannelID  int    `json:"channelID"`
	UserID     int    `json:"userID"`
	Username   string `json:"username"`
	ServerTime int64  `json:"serverTime"`
}

// Pong answers Ping, echoing the client timestamp.
type Pong struct {
	Data PingData `json:"data"`
}

// Kicked precedes a server-initiated disconnect of this user. Reason is
// optional on the wire; KickedByUserID is 0 for a system kick.
type Kicked struct {
	ChannelID      int    `json:"channelID"`
	KickedByUserID int    `json:"kickedByUserID"`
	Reason         string `json:"reason"`
}

// IdleTimeout precedes a disconnect for 24h without chatting; the close
// reason "idle timeout" must not trigger auto-reconnect.
type IdleTimeout struct {
	Reason string `json:"reason"`
}

// Revalidate is the server's nudge shortly before the chat JWT's hard
// close deadline. Answering with Reauthenticate carrying a fresh token
// swaps the socket's claims in place; ignoring it leaves the unchanged
// close-and-reconnect path, since the deadline never moves.
type Revalidate struct {
	ServerTime int64 `json:"serverTime"`
}

// Revalidated acknowledges a successful Reauthenticate. The privilege
// flags come from the new token and replace those from Authenticated, so
// a mod demoted on the site loses the flags without reconnecting.
type Revalidated struct {
	IsAdmin    bool  `json:"isAdmin"`
	IsChatMod  bool  `json:"isChatMod"`
	IsSiteMod  bool  `json:"isSiteMod"`
	ServerTime int64 `json:"serverTime"`
}

// Unknown is any frame this client does not (yet) understand, including
// known names whose payload failed to decode. Callers must treat it as
// ignorable — that tolerance is what lets old binaries survive protocol
// additions (ADR 0002).
type Unknown struct {
	Name string
}

// Decode maps a server frame to its typed struct. It never fails:
// unrecognized names and undecodable payloads come back as Unknown.
func Decode(data []byte) any {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Unknown{}
	}
	as := func(v any) any {
		if err := json.Unmarshal(data, v); err != nil {
			return Unknown{Name: env.Name}
		}
		return v
	}
	switch env.Name {
	case "authenticated":
		return deref(as(&Authenticated{}))
	case "unauthorized":
		return deref(as(&Unauthorized{}))
	case "error":
		return deref(as(&Error{}))
	case "channelID":
		return deref(as(&ChannelID{}))
	case "subscribed":
		return deref(as(&Subscribed{}))
	case "unsubscribed":
		return deref(as(&Unsubscribed{}))
	case "message", "meMessage", "slapMessage", "serverMessage",
		"directMessage":
		return deref(as(&Message{}))
	case "typing":
		return deref(as(&TypingEvent{}))
	case "userJoined":
		return deref(as(&UserJoined{}))
	case "userLeft":
		return deref(as(&UserLeft{}))
	case "userUpdated":
		return deref(as(&UserUpdated{}))
	case "revalidate":
		return deref(as(&Revalidate{}))
	case "revalidated":
		return deref(as(&Revalidated{}))
	case "pong":
		return deref(as(&Pong{}))
	case "kicked":
		return deref(as(&Kicked{}))
	case "idleTimeout":
		return deref(as(&IdleTimeout{}))
	default:
		return Unknown{Name: env.Name}
	}
}

// deref unwraps the pointer produced by Decode's helper so consumers can
// type-switch on values.
func deref(v any) any {
	switch t := v.(type) {
	case *Authenticated:
		return *t
	case *Unauthorized:
		return *t
	case *Error:
		return *t
	case *ChannelID:
		return *t
	case *Subscribed:
		return *t
	case *Unsubscribed:
		return *t
	case *Message:
		return *t
	case *TypingEvent:
		return *t
	case *UserJoined:
		return *t
	case *UserLeft:
		return *t
	case *Pong:
		return *t
	case *Kicked:
		return *t
	case *IdleTimeout:
		return *t
	case *UserUpdated:
		return *t
	case *Revalidate:
		return *t
	case *Revalidated:
		return *t
	default:
		return v
	}
}
