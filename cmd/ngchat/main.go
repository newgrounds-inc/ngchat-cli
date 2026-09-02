// Command ngchat is a minimalist terminal client for Newgrounds Chat.
package main

import (
	"bufio"
	"context"
	"errors"
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
	defaultWSURL   = "wss://chat.newgrounds.com/ws"
	defaultSiteURL = "https://www.newgrounds.com"
	signedOutHint  = "signed out (password changed, or logged out); " +
		"run `ngchat login`"
)

func main() {
	channel := flag.String("channel", "general", "channel to join")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("ngchat", version)
		return
	}

	wsURL := envOr("NGCHAT_WS_URL", defaultWSURL)
	// Dev/staging proxy routing cookie (e.g. "serverid=..."), required
	// on every request against the non-prod stack.
	routing := os.Getenv("NGCHAT_ROUTING_COOKIE")
	store := auth.Store{}

	ctx, cancel := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer cancel()

	site, err := newSite(routing)
	if err != nil {
		fatal(err)
	}

	switch flag.Arg(0) {
	case "login":
		if _, err := login(ctx, store, site); err != nil {
			fatal(err)
		}
		return
	case "logout":
		if err := store.Delete(auth.RememberKey); err != nil {
			fatal(err)
		}
		if _, err := store.Migrate(); err != nil {
			fatal(err)
		}
		fmt.Println("credentials cleared")
		return
	case "":
	default:
		usage()
		os.Exit(2)
	}

	if err := prepareSite(ctx, store, site); err != nil {
		fatal(err)
	}

	chat := client.New(client.Config{
		WSURL:   wsURL,
		Channel: *channel,
		Minter:  &auth.ServiceTokenMinter{Site: site},
		Cookie:  routing,
	})
	go chat.Run(ctx)

	prog := tea.NewProgram(ui.New(chat, strings.ToLower(*channel)),
		tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil && ctx.Err() == nil {
		fatal(err)
	}
	if m, ok := final.(ui.Model); ok && m.SignedOut() {
		fatal(errors.New(signedOutHint))
	}
}

// newSite builds the site client for the run, seeding the dev proxy
// routing cookie so it rides on every request like it does in a browser.
func newSite(routing string) (*auth.Site, error) {
	site, err := auth.NewSite(envOr("NGCHAT_SITE_URL", defaultSiteURL))
	if err != nil {
		return nil, err
	}
	if routing != "" {
		if err := site.SeedCookies(routing); err != nil {
			return nil, fmt.Errorf("NGCHAT_ROUTING_COOKIE: %w", err)
		}
	}
	return site, nil
}

// prepareSite gets the jar into a state that can mint: a raw cookie
// header from the environment (dev and smoke), else the stored remember
// cookie, else a first-run login. A v0.1 cookie-header slot is removed
// on the way, since it cannot be converted (ADR 0003).
func prepareSite(ctx context.Context, store auth.Store, site *auth.Site) error {
	if header := os.Getenv("NGCHAT_NG_COOKIE"); header != "" {
		if err := site.SeedCookies(header); err != nil {
			return fmt.Errorf("NGCHAT_NG_COOKIE: %w", err)
		}
		return nil
	}
	removed, err := store.Migrate()
	if err != nil {
		return err
	}
	if removed {
		fmt.Fprintln(os.Stderr, "ngchat: the cookie stored by an older "+
			"version was removed; please log in once")
	}
	remember, err := store.Load(auth.RememberKey)
	if errors.Is(err, auth.ErrNotFound) {
		// The login leaves the jar authenticated, so the first mint
		// needs no further setup.
		_, err := login(ctx, store, site)
		return err
	}
	if err != nil {
		return err
	}
	site.SetRemember(remember)
	return nil
}

// login runs the interactive flow and persists the remember cookie.
func login(ctx context.Context, store auth.Store, site *auth.Site) (auth.Session, error) {
	sess, err := auth.Login(ctx, site, termPrompter{
		in: bufio.NewReader(os.Stdin)}, os.Stdout)
	if err != nil {
		return auth.Session{}, err
	}
	if err := store.Save(auth.RememberKey, sess.Remember); err != nil {
		return auth.Session{}, fmt.Errorf("storing credential: %w", err)
	}
	fmt.Printf("logged in as %s\n", sess.User.Username)
	return sess, nil
}

// termPrompter reads login input from the terminal, hiding secrets when
// stdin is a TTY and reading plain lines otherwise (piped input).
type termPrompter struct {
	in *bufio.Reader
}

func (p termPrompter) Line(prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (p termPrompter) Secret(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return p.Line(prompt)
	}
	fmt.Print(prompt)
	raw, err := term.ReadPassword(fd)
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
  ngchat [flags]              connect and chat (prompts for login the first time)
  ngchat login                log in and store the remember cookie
  ngchat logout               clear stored credentials

flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
environment:
  NGCHAT_WS_URL           chat WebSocket URL (default %s)
  NGCHAT_SITE_URL         site base URL (default %s)
  NGCHAT_ROUTING_COOKIE   extra cookie for the dev/staging proxy
  NGCHAT_NG_COOKIE        NG cookie header (bypasses the stored login)
`, defaultWSURL, defaultSiteURL)
}

// fatal prints an error and exits non-zero.
func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ngchat:", err)
	os.Exit(1)
}
