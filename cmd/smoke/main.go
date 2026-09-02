// Command smoke is a headless harness for verifying the client stack
// against a live stack: mint → connect → authenticate → subscribe →
// send. It prints event summaries only — never tokens or cookies.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/protocol"
	"github.com/newgrounds-inc/ngchat-cli/internal/render"
)

func main() {
	routing := os.Getenv("NGCHAT_ROUTING_COOKIE")
	site, err := auth.NewSite(os.Getenv("NGCHAT_SITE_URL"))
	if err != nil {
		fmt.Println("NGCHAT_SITE_URL:", err)
		os.Exit(2)
	}
	if routing != "" {
		if err := site.SeedCookies(routing); err != nil {
			fmt.Println("NGCHAT_ROUTING_COOKIE:", err)
			os.Exit(2)
		}
	}
	// SMOKE_NG_COOKIE is a raw cookie header; without it the stored
	// remember cookie from `ngchat login` is used, which is how the
	// login flow itself gets verified end to end. The source is printed
	// because a stale export silently wins over a fresh login.
	if header := os.Getenv("SMOKE_NG_COOKIE"); header != "" {
		if err := site.SeedCookies(header); err != nil {
			fmt.Println("SMOKE_NG_COOKIE:", err)
			os.Exit(2)
		}
		fmt.Println("credential: SMOKE_NG_COOKIE from the environment " +
			"(unset it to use the stored login)")
	} else {
		remember, err := (auth.Store{}).Load(auth.RememberKey)
		if err != nil {
			fmt.Println("no SMOKE_NG_COOKIE and no stored login:", err)
			os.Exit(2)
		}
		site.SetRemember(remember)
		fmt.Println("credential: stored login")
	}
	minter := &auth.ServiceTokenMinter{Site: site}

	// SMOKE_SECONDS extends the run for renewal checks: with the site's
	// APP_JWT_CHAT_TTL at ~90s, 150s is enough to see revalidate →
	// revalidated without a reconnect.
	seconds := 15
	if v, err := strconv.Atoi(os.Getenv("SMOKE_SECONDS")); err == nil && v > 0 {
		seconds = v
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(seconds)*time.Second)
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
			fmt.Printf("subscribed channelID=%d buffer=%d users=%d notices=%d gap=%v\n",
				msg.ChannelID, len(msg.MessageBuffer), len(msg.UserList),
				len(msg.Notifications), e.Gap)
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
		case protocol.UserUpdated:
			fmt.Printf("userUpdated %s mod=%v away=%v\n",
				msg.Username, msg.IsChatMod, msg.IsAway)
		case protocol.Revalidated:
			fmt.Printf("revalidated admin=%v chatMod=%v siteMod=%v\n",
				msg.IsAdmin, msg.IsChatMod, msg.IsSiteMod)
		case client.RenewalFailed:
			fmt.Printf("renewal failed: %v\n", msg.Err)
		case protocol.Unknown:
			fmt.Printf("UNKNOWN/undecodable frame: name=%q\n", msg.Name)
		case nil:
			fmt.Printf("state=%v err=%v\n", e.State, e.Err)
			var denied *client.AccessDenied
			if errors.As(e.Err, &denied) {
				fmt.Printf("access denied: %s\n", render.Text(denied.Message))
			}
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
