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
// the only history a client ever receives. UserList is the full roster at
// subscribe time, the baseline that UserJoined, UserLeft, UserUpdated and
// Away then patch. Notifications are the server's persistent inbox of
// mentions and DMs received while away, newest first, up to 100; the
// server never clears it, so every subscribe carries the same rows again.
type Subscribed struct {
	ChannelID     int               `json:"channelID"`
	ChannelName   string            `json:"channelName"`
	MessageBuffer []json.RawMessage `json:"messageBuffer"`
	UserList      []ChannelUser     `json:"userList"`
	Notifications []Notification    `json:"notifications"`
	ServerTime    int64             `json:"serverTime"`
}

// ChannelUser is one roster entry. AwayMessage is server-rendered HTML,
// like message bodies; AwayMessageRaw is the text the user typed.
type ChannelUser struct {
	UserID         int    `json:"userID"`
	Username       string `json:"username"`
	IsAdmin        bool   `json:"isAdmin"`
	IsChatMod      bool   `json:"isChatMod"`
	IsAway         bool   `json:"isAway"`
	AwayMessage    string `json:"awayMessage"`
	AwayMessageRaw string `json:"awayMessageRaw"`
	UserIcon       string `json:"userIcon"`
	UserPageURL    string `json:"userPageURL"`
}

// Notification is one inbox row from Subscribed: a mention or DM that
// reached the user while away. MessageType is the server's notification
// category, not the frame name: "message", "meMessage" (which also covers
// a mention inside an away message), "directMessage", "modDirectMessage"
// or "serverMessage". Message is rendered HTML.
type Notification struct {
	Message     string `json:"message"`
	MessageRaw  string `json:"messageRaw"`
	MessageType string `json:"messageType"`
	ServerTime  int64  `json:"serverTime"`
	UserID      int    `json:"userID"`
	Username    string `json:"username"`
}

// Away reports a user going away (IsAway true, with a message) or coming
// back (IsAway false). It patches the roster entry; it also appears in
// the backfill buffer, where the DB copy omits isAway entirely, so
// backfilled ones are ambiguous and are not replayed.
type Away struct {
	ChannelID      int    `json:"channelID"`
	UserID         int    `json:"userID"`
	Username       string `json:"username"`
	IsAdmin        bool   `json:"isAdmin"`
	IsChatMod      bool   `json:"isChatMod"`
	IsAway         bool   `json:"isAway"`
	AwayMessage    string `json:"awayMessage"`
	AwayMessageRaw string `json:"awayMessageRaw"`
	ServerTime     int64  `json:"serverTime"`
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
	ChannelID      int    `json:"channelID"`
	UserID         int    `json:"userID"`
	Username       string `json:"username"`
	IsAdmin        bool   `json:"isAdmin"`
	IsChatMod      bool   `json:"isChatMod"`
	IsAway         bool   `json:"isAway"`
	AwayMessage    string `json:"awayMessage"`
	AwayMessageRaw string `json:"awayMessageRaw"`
	UserIcon       string `json:"userIcon"`
	UserPageURL    string `json:"userPageURL"`
	ServerTime     int64  `json:"serverTime"`
}

// UserUpdated is a silent roster patch: the same shape as UserJoined but
// for a user already in the channel (a mid-session role or away change,
// typically published by an in-place token renewal). Consumers must not
// announce it as a join; the UI's user list applies it as a patch.
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
	ServerTime     int64  `json:"serverTime"`
}

// IdleTimeout precedes a disconnect for 24h without chatting; the close
// reason "idle timeout" must not trigger auto-reconnect.
type IdleTimeout struct {
	Reason     string `json:"reason"`
	ServerTime int64  `json:"serverTime"`
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

// FrameName returns the name discriminator of any frame, client or
// server, or "" when the bytes are not a named frame. Loggers use it to
// label frames without decoding them.
func FrameName(data []byte) string {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return ""
	}
	return env.Name
}

// Decode maps a server frame to its typed struct. It never fails:
// unrecognized names and undecodable payloads come back as Unknown.
func Decode(data []byte) any {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Unknown{}
	}
	switch env.Name {
	case "authenticated":
		return decodeAs[Authenticated](data, env.Name)
	case "unauthorized":
		return decodeAs[Unauthorized](data, env.Name)
	case "error":
		return decodeAs[Error](data, env.Name)
	case "channelID":
		return decodeAs[ChannelID](data, env.Name)
	case "subscribed":
		return decodeAs[Subscribed](data, env.Name)
	case "unsubscribed":
		return decodeAs[Unsubscribed](data, env.Name)
	case "message", "meMessage", "slapMessage", "serverMessage",
		"directMessage":
		return decodeAs[Message](data, env.Name)
	case "typing":
		return decodeAs[TypingEvent](data, env.Name)
	case "userJoined":
		return decodeAs[UserJoined](data, env.Name)
	case "userLeft":
		return decodeAs[UserLeft](data, env.Name)
	case "userUpdated":
		return decodeAs[UserUpdated](data, env.Name)
	case "away":
		return decodeAs[Away](data, env.Name)
	case "revalidate":
		return decodeAs[Revalidate](data, env.Name)
	case "revalidated":
		return decodeAs[Revalidated](data, env.Name)
	case "pong":
		return decodeAs[Pong](data, env.Name)
	case "kicked":
		return decodeAs[Kicked](data, env.Name)
	case "idleTimeout":
		return decodeAs[IdleTimeout](data, env.Name)
	default:
		return Unknown{Name: env.Name}
	}
}

// decodeAs returns a T by value so consumers can type-switch on values;
// a payload that does not fit becomes Unknown, per the drift policy.
func decodeAs[T any](data []byte, name string) any {
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return Unknown{Name: name}
	}
	return v
}
