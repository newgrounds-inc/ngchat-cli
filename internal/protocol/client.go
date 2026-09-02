// Package protocol hand-ports the NG Chat wire types from the server's Zod
// schemas (ngchat repo, src/shared/protocol). The server validates client
// messages with strict schemas — unknown fields are rejected — so the
// structs in this file must stay exactly in sync with the server. Server
// messages are decoded tolerantly instead (see server.go), so server-side
// additions never break this client.
package protocol

// Authenticate must be the first message sent on a new socket. Only
// Authenticate and Ping are accepted pre-auth; the server closes
// unauthenticated sockets after 15s.
type Authenticate struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// NewAuthenticate builds an authenticate message for the given chat JWT.
func NewAuthenticate(token string) Authenticate {
	return Authenticate{Name: "authenticate", Token: token}
}

// Reauthenticate answers a server Revalidate with a freshly minted chat
// JWT. It is deliberately not Authenticate: the server replaces the
// socket's claim snapshot in place instead of re-running the
// join/subscribe/motd pipeline, and Authenticate's already-authenticated
// guard stays. The server refuses a token for a different account or one
// whose expiry is not strictly later than the token in force, and caps
// attempts per token (~5), so send at most one per Revalidate.
type Reauthenticate struct {
	Name  string `json:"name"`
	Token string `json:"token"`
}

// NewReauthenticate builds the in-place renewal message.
func NewReauthenticate(token string) Reauthenticate {
	return Reauthenticate{Name: "reauthenticate", Token: token}
}

// ChatMessage sends a chat line (1..5000 chars) to a subscribed channel.
// Slash commands (/me, /dm, ...) are plain message text parsed server-side.
type ChatMessage struct {
	Name      string `json:"name"`
	ChannelID int    `json:"channelID"`
	Message   string `json:"message"`
}

// NewChatMessage builds a message send for a channel.
func NewChatMessage(channelID int, text string) ChatMessage {
	return ChatMessage{Name: "message", ChannelID: channelID, Message: text}
}

// GetChannelID resolves a channel name to its numeric ID. Unknown names
// come back as an Error with a RedirectChannel.
type GetChannelID struct {
	Name        string `json:"name"`
	ChannelName string `json:"channelName"`
}

// NewGetChannelID builds a channel-name lookup. Names are lowercase on the
// server; callers should lowercase before sending.
func NewGetChannelID(channelName string) GetChannelID {
	return GetChannelID{Name: "getChannelID", ChannelName: channelName}
}

// PingData carries the client timestamp echoed back in Pong.
type PingData struct {
	Time int64 `json:"time"`
}

// Ping is the application-level heartbeat. The server terminates any
// socket with no inbound traffic for 15 seconds, so clients must ping
// continuously (the browser client uses a 1-5s cadence).
type Ping struct {
	Name string   `json:"name"`
	Data PingData `json:"data"`
}

// NewPing builds a heartbeat ping stamped with epoch milliseconds.
func NewPing(unixMilli int64) Ping {
	return Ping{Name: "ping", Data: PingData{Time: unixMilli}}
}

// Subscribe joins a channel by ID; the reply is Subscribed with the
// backfill buffer and user list.
type Subscribe struct {
	Name      string `json:"name"`
	ChannelID int    `json:"channelID"`
}

// NewSubscribe builds a channel subscription.
func NewSubscribe(channelID int) Subscribe {
	return Subscribe{Name: "subscribe", ChannelID: channelID}
}

// Typing signals the user is composing; exempt from flood limits.
type Typing struct {
	Name      string `json:"name"`
	ChannelID int    `json:"channelID"`
}

// NewTyping builds a typing indicator for a channel.
func NewTyping(channelID int) Typing {
	return Typing{Name: "typing", ChannelID: channelID}
}

// Unsubscribe leaves a channel.
type Unsubscribe struct {
	Name      string `json:"name"`
	ChannelID int    `json:"channelID"`
}

// NewUnsubscribe builds a channel unsubscription.
func NewUnsubscribe(channelID int) Unsubscribe {
	return Unsubscribe{Name: "unsubscribe", ChannelID: channelID}
}
