// Command ngchat is a minimalist terminal client for Newgrounds Chat.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/ui"
)

// version is stamped by GoReleaser.
var version = "dev"

const (
	defaultWSURL  = "wss://chat.newgrounds.com/ws"
	defaultJWTURL = "https://www.newgrounds.com/ngapps/jwt.php"
	// rememberCookieKey is the credential-store slot for the NG cookie
	// header used to re-mint chat JWTs (ADR 0001).
	rememberCookieKey = "ng_cookie"
)

func main() {
	channel := flag.String("channel", "general", "channel to join")
	user := flag.String("user", "", "NG username (password login)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("ngchat", version)
		return
	}

	wsURL := envOr("NGCHAT_WS_URL", defaultWSURL)
	jwtURL := envOr("NGCHAT_JWT_URL", defaultJWTURL)
	// Dev/staging proxy routing cookie (e.g. "serverid=..."), required
	// on every request against the non-prod stack.
	routing := os.Getenv("NGCHAT_ROUTING_COOKIE")
	store := auth.Store{}

	switch flag.Arg(0) {
	case "set-cookie":
		if err := setCookie(store); err != nil {
			fatal(err)
		}
		return
	case "logout":
		if err := store.Delete(rememberCookieKey); err != nil {
			fatal(err)
		}
		fmt.Println("credentials cleared")
		return
	case "":
	default:
		usage()
		os.Exit(2)
	}

	minter, err := buildMinter(store, jwtURL, routing, *user)
	if err != nil {
		fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	chat := client.New(client.Config{
		WSURL:   wsURL,
		Channel: *channel,
		Minter:  minter,
		Cookie:  routing,
	})
	go chat.Run(ctx)

	prog := tea.NewProgram(ui.New(chat, strings.ToLower(*channel)),
		tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := prog.Run(); err != nil && ctx.Err() == nil {
		fatal(err)
	}
}

// buildMinter picks the JWT source: a stored NG cookie when present (the
// long-term path for all users), otherwise interactive username/password
// (interim, allowlisted bot accounts only — ADR 0001).
func buildMinter(store auth.Store, jwtURL, routing, user string) (auth.Minter, error) {
	if cookie := os.Getenv("NGCHAT_NG_COOKIE"); cookie != "" {
		return &auth.CookieMinter{
			JWTURL:   jwtURL,
			NGCookie: cookie,
			Cookie:   routing,
		}, nil
	}
	if cookie, err := store.Load(rememberCookieKey); err == nil {
		return &auth.CookieMinter{
			JWTURL:   jwtURL,
			NGCookie: cookie,
			Cookie:   routing,
		}, nil
	}

	username := user
	if username == "" {
		fmt.Print("NG username: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return nil, err
		}
		username = strings.TrimSpace(line)
	}
	fmt.Print("NG password (memory only, never stored): ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return nil, err
	}
	return &auth.PasswordMinter{
		JWTURL:   jwtURL,
		Username: username,
		Password: string(pw),
		Cookie:   routing,
	}, nil
}

// setCookie stashes a manually-obtained NG cookie header into the
// credential store — the interim escape hatch for non-allowlisted users
// until the site's CLI login endpoints ship.
func setCookie(store auth.Store) error {
	fmt.Print("NG cookie header value (input hidden): ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return err
	}
	cookie := strings.TrimSpace(string(raw))
	if cookie == "" {
		return fmt.Errorf("empty cookie")
	}
	if err := store.Save(rememberCookieKey, cookie); err != nil {
		return err
	}
	fmt.Println("cookie stored; future runs will use it to mint chat tokens")
	return nil
}

// envOr reads an environment variable with a default.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func usage() {
	fmt.Fprintf(os.Stderr, `ngchat — Newgrounds Chat in your terminal

usage:
  ngchat [flags]              connect and chat
  ngchat set-cookie           store an NG cookie for token minting
  ngchat logout               clear stored credentials

flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
environment:
  NGCHAT_WS_URL           chat WebSocket URL (default %s)
  NGCHAT_JWT_URL          jwt.php URL (default %s)
  NGCHAT_ROUTING_COOKIE   extra cookie for the dev/staging proxy
  NGCHAT_NG_COOKIE        NG cookie header (overrides the stored one)
`, defaultWSURL, defaultJWTURL)
}

// fatal prints an error and exits non-zero.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ngchat:", err)
	os.Exit(1)
}
