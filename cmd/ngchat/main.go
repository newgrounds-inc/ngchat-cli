// Command ngchat is a minimalist terminal client for Newgrounds Chat.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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
	// channel is the one room this client joins. There is one channel on
	// the server today; switching is out of scope for the MVP.
	channel       = "general"
	signedOutHint = "signed out (password changed, or logged out); " +
		"run `ngchat login`"
)

func main() {
	quiet := flag.Bool("quiet", false, "no terminal bell on mentions and DMs")
	debug := flag.Bool("debug", false,
		"write redacted frames and state changes to the debug log")
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
		if _, err := store.Migrate(); err != nil {
			fatal(err)
		}
		if _, err := login(ctx, store, site); err != nil {
			exitLogin(ctx, err)
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
		exitLogin(ctx, err)
	}
	if err := runChat(ctx, chatOptions{
		wsURL: wsURL, routing: routing, site: site,
		quiet: *quiet, debug: *debug,
	}); err != nil {
		fatal(err)
	}
}

// chatOptions carries what runChat needs from the flags and environment.
type chatOptions struct {
	wsURL   string
	routing string
	site    *auth.Site
	quiet   bool
	debug   bool
}

// runChat runs the client and the TUI until one of them ends. It returns
// the error for main to report rather than exiting itself, so the debug
// log is closed and its path printed on every exit, including the
// signed-out one a user is most likely to report.
func runChat(ctx context.Context, opts chatOptions) error {
	var logger *slog.Logger
	if opts.debug {
		path, err := debugLogPath()
		if err != nil {
			return err
		}
		f, err := openDebugLog(path)
		if err != nil {
			return err
		}
		defer f.Close()
		// Printed after the alt screen is gone so it is the last line on
		// the terminal, where the user will look for it.
		defer fmt.Fprintln(os.Stderr, "ngchat: debug log written to", path)
		logger = slog.New(slog.NewTextHandler(f,
			&slog.HandlerOptions{Level: slog.LevelDebug}))
		logger.Info("ngchat start", "version", version, "ws", opts.wsURL)
	}

	chat := client.New(client.Config{
		WSURL:   opts.wsURL,
		Channel: channel,
		Minter:  &auth.ServiceTokenMinter{Site: opts.site},
		Cookie:  opts.routing,
		Log:     logger,
	})
	go chat.Run(ctx)

	prog := tea.NewProgram(
		ui.New(chat, ui.Options{Channel: channel, Quiet: opts.quiet}),
		tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil && ctx.Err() == nil {
		return err
	}
	if m, ok := final.(ui.Model); ok && m.SignedOut() {
		return errors.New(signedOutHint)
	}
	return nil
}

// debugLogPath is the fixed location of the -debug log: the platform's
// per-user state directory, so it is never in the working directory a
// user might commit or share by accident. The path is fixed rather than
// per-run so a bug report can name it without a directory listing.
func debugLogPath() (string, error) {
	var base string
	switch {
	case os.Getenv("XDG_STATE_HOME") != "":
		base = os.Getenv("XDG_STATE_HOME")
	case runtime.GOOS == "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, "Library", "Logs")
	case runtime.GOOS == "windows":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		base = dir
	default:
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ngchat", "debug.log"), nil
}

// openDebugLog truncates and opens the log at path, creating its
// directory. Mode 0600: frames are redacted but chat text is still the
// user's private conversation.
func openDebugLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("debug log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("debug log: %w", err)
	}
	return f, nil
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

// exitLogin ends a failed login. An interrupt at a prompt is the user
// leaving, not a failure to report.
func exitLogin(ctx context.Context, err error) {
	if ctx.Err() != nil {
		os.Exit(130)
	}
	fatal(err)
}

// login runs the interactive flow and persists the remember cookie.
func login(ctx context.Context, store auth.Store, site *auth.Site) (auth.Session, error) {
	sess, err := auth.Login(ctx, site, termPrompter{
		ctx: ctx,
		in:  bufio.NewReader(os.Stdin),
		fd:  int(os.Stdin.Fd()),
	}, os.Stdout)
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
// stdin is a TTY and reading plain lines otherwise (piped input). Reads
// run on a goroutine so Ctrl-C (which cancels ctx) ends the prompt
// instead of restarting the blocked read; the abandoned reader is
// harmless because the flow aborts and the process exits.
type termPrompter struct {
	ctx context.Context
	in  *bufio.Reader
	fd  int
}

type readResult struct {
	text string
	err  error
}

func (p termPrompter) Line(prompt string) (string, error) {
	fmt.Print(prompt)
	ch := make(chan readResult, 1)
	go func() {
		line, err := p.in.ReadString('\n')
		if err != nil && line == "" {
			ch <- readResult{err: err}
			return
		}
		ch <- readResult{text: strings.TrimSpace(line)}
	}()
	return p.wait(ch, nil)
}

func (p termPrompter) Secret(prompt string) (string, error) {
	if !term.IsTerminal(p.fd) {
		return p.Line(prompt)
	}
	fmt.Print(prompt)
	// ReadPassword restores the terminal itself on a normal return; on
	// cancellation nothing else would, so keep the state to restore.
	state, err := term.GetState(p.fd)
	if err != nil {
		return "", err
	}
	ch := make(chan readResult, 1)
	go func() {
		raw, err := term.ReadPassword(p.fd)
		ch <- readResult{text: string(raw), err: err}
	}()
	text, err := p.wait(ch, func() { _ = term.Restore(p.fd, state) })
	fmt.Println()
	return text, err
}

// wait returns the read result or, if ctx ends first, ctx's error after
// running cleanup.
func (p termPrompter) wait(ch <-chan readResult, cleanup func()) (string, error) {
	select {
	case r := <-ch:
		return r.text, r.err
	case <-p.ctx.Done():
		if cleanup != nil {
			cleanup()
		}
		return "", p.ctx.Err()
	}
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
  ngchat [flags]              join #general (prompts for login the first time)
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
