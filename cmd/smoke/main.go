// Command smoke is a headless harness for verifying the client stack
// against a live stack: mint → connect → authenticate → subscribe →
// send. It prints event summaries only — never tokens or cookies.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
	"github.com/newgrounds-inc/ngchat-cli/internal/render"
)

func main() {
	routing := os.Getenv("NGCHAT_ROUTING_COOKIE")
	minter := &auth.CookieMinter{
		JWTURL:   os.Getenv("NGCHAT_JWT_URL"),
		NGCookie: os.Getenv("SMOKE_NG_COOKIE"),
		Cookie:   routing,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := client.New(client.Config{
		WSURL:   os.Getenv("NGCHAT_WS_URL"),
		Channel: "general",
		Minter:  minter,
		Cookie:  routing,
	})
	go c.Run(ctx)

	sent := false
	for e := range c.Events() {
		switch msg := e.Msg.(type) {
		case protocol.Authenticated:
			fmt.Printf("authenticated as %s (userID %d, motd %q)\n",
				msg.Username, msg.UserID, msg.MOTDText())
		case protocol.Subscribed:
			fmt.Printf("subscribed channelID=%d buffer=%d gap=%v\n",
				msg.ChannelID, len(msg.MessageBuffer), e.Gap)
			if !sent {
				sent = true
				if err := c.SendChat("ngchat-cli smoke test — hello from Go"); err != nil {
					fmt.Println("send failed:", err)
				} else {
					fmt.Println("sent test message")
				}
			}
		case protocol.Message:
			fmt.Printf("%s <%s> spoiler=%v id=%v | %s\n",
				msg.Name, msg.Username, msg.IsSpoiler, deref(msg.ID),
				truncate(render.Text(msg.Message), 80))
		case protocol.TypingEvent:
			fmt.Printf("typing channelID=%d userID=%d username=%q\n",
				msg.ChannelID, msg.UserID, msg.Username)
		case protocol.UserJoined:
			fmt.Printf("userJoined %s\n", msg.Username)
		case protocol.UserLeft:
			fmt.Printf("userLeft %s\n", msg.Username)
		case protocol.Unknown:
			fmt.Printf("UNKNOWN/undecodable frame: name=%q\n", msg.Name)
		case nil:
			fmt.Printf("state=%v err=%v\n", e.State, e.Err)
		default:
			fmt.Printf("event %T\n", msg)
		}
	}
	fmt.Println("done")
}

// deref renders an optional message ID for logging.
func deref(id *int64) any {
	if id == nil {
		return "nil"
	}
	return *id
}

// truncate bounds log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
