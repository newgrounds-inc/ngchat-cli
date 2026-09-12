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
	"github.com/newgrounds-inc/ngchat-cli/internal/run"
)

func main() {
	env := run.FromEnv()
	site, err := run.NewSite(env)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}

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

	// No login fallback: the harness is headless, so with nothing usable
	// stored it stops and says what to do. The source is printed because
	// an exported NGCHAT_NG_COOKIE silently wins over a fresh login.
	source, err := run.Prepare(ctx, run.Options{
		Env: env, Store: auth.Store{}, Site: site, Notices: os.Stdout,
	})
	if err != nil {
		fmt.Println(err)
		if errors.Is(err, run.ErrNoCredential) {
			fmt.Println("run `ngchat login` against this site, or export " +
				"NGCHAT_NG_COOKIE")
		}
		os.Exit(2)
	}
	switch source {
	case run.FromHeader:
		fmt.Println("credential:", source,
			"(unset it to use the stored login)")
	default:
		fmt.Println("credential:", source)
	}

	c := client.New(run.ClientConfig(env, site, nil))
	go c.Run(ctx)

	sent := false
	for e := range c.Events() {
		switch msg := e.Msg.(type) {
		case protocol.Authenticated:
			fmt.Printf("authenticated as %s (userID %d, motd %q)\n",
				render.Line(msg.Username), msg.UserID, msg.MOTDText())
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
				msg.Name, render.Line(msg.Username), msg.IsSpoiler, deref(msg.ID),
				truncate(render.Text(msg.Message), 80))
		case protocol.TypingEvent:
			fmt.Printf("typing channelID=%d userID=%d username=%q\n",
				msg.ChannelID, msg.UserID, msg.Username)
		case protocol.UserJoined:
			fmt.Printf("userJoined %s\n", render.Line(msg.Username))
		case protocol.UserLeft:
			fmt.Printf("userLeft %s\n", render.Line(msg.Username))
		case protocol.UserUpdated:
			fmt.Printf("userUpdated %s mod=%v away=%v\n",
				render.Line(msg.Username), msg.IsChatMod, msg.IsAway)
		case protocol.Revalidated:
			fmt.Printf("revalidated admin=%v chatMod=%v siteMod=%v\n",
				msg.IsAdmin, msg.IsChatMod, msg.IsSiteMod)
		case client.RenewalFailed:
			fmt.Printf("renewal failed: %s\n", render.Line(msg.Err.Error()))
		case protocol.Unknown:
			fmt.Printf("UNKNOWN/undecodable frame: name=%q\n", msg.Name)
		case nil:
			fmt.Printf("state=%v err=%s\n", e.State, render.Line(fmt.Sprint(e.Err)))
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
