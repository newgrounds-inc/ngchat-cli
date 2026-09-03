// Command ngchat is a minimalist terminal client for Newgrounds Chat.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"golang.org/x/term"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
	"github.com/newgrounds-inc/ngchat-cli/internal/render"
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
		os.Exit(logout(ctx, store, site, os.Stdout, os.Stderr))
	case "":
	default:
		usage()
		os.Exit(2)
	}

	// The chat URL matters only on the chat path, and is checked before
	// any mint because the token minted from the site's cookie goes to
	// it as the first frame. A stale NGCHAT_WS_URL must not block login
	// or logout.
	if err := site.CheckChatURL(wsURL); err != nil {
		fatal(fmt.Errorf("NGCHAT_WS_URL: %w", err))
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

// credentialStore and revoker are what logout needs from auth.Store and
// *auth.Site, narrowed so the outcome matrix can be tested with fakes.
type credentialStore interface {
	Load(key string) (string, error)
	Delete(key string) error
	Migrate() (bool, error)
}

type revoker interface {
	CredentialKey() string
	SetRemember(value string)
	Logout(ctx context.Context) error
}

// logout revokes this device's remember token on the site, then clears
// every local copy, and returns the exit code. Local cleanup always
// runs, whatever the earlier steps did. The exit is zero only when the
// site revoked the cookie (or there was provably none to revoke) and
// both local backends are confirmed clear; the one downgrade is a
// keyring that could not be checked (ErrKeyringUnavailable) after the
// site has revoked the cookie, since whatever it may hold is dead then.
// Without that proof, an unreadable keyring may hold a live credential
// and the command says so and fails.
func logout(ctx context.Context, store credentialStore, site revoker,
	out, errOut io.Writer) int {
	key := site.CredentialKey()
	remember, loadErr := store.Load(key)
	stored := loadErr == nil
	revoked := false
	var revokeErr error
	if stored {
		site.SetRemember(remember)
		revokeErr = site.Logout(ctx)
		revoked = revokeErr == nil
	}

	clearErr := store.Delete(key)
	if _, err := store.Migrate(); err != nil {
		clearErr = errors.Join(clearErr, err)
	}

	code := 0
	if loadErr != nil && !errors.Is(loadErr, auth.ErrNotFound) {
		code = 1
		fmt.Fprintln(errOut, "ngchat: could not read the stored credential, "+
			"so nothing was revoked on the site:", loadErr)
	}
	if revokeErr != nil {
		code = 1
		fmt.Fprintln(errOut, "ngchat: the site did not revoke the cookie:",
			revokeErr)
		fmt.Fprintln(errOut, "ngchat: it stays valid there until you log "+
			"out again online or change your password")
	}
	switch {
	case clearErr == nil:
	case errors.Is(clearErr, auth.ErrKeyringUnavailable) && revoked:
		fmt.Fprintln(errOut, "ngchat:", clearErr)
	case errors.Is(clearErr, auth.ErrKeyringUnavailable):
		code = 1
		fmt.Fprintln(errOut, "ngchat:", clearErr)
		fmt.Fprintln(errOut, "ngchat: a credential it may hold was not "+
			"revoked; unlock it and run `ngchat logout` again")
	default:
		code = 1
		fmt.Fprintln(errOut, "ngchat: local credentials not fully cleared:",
			clearErr)
	}
	switch {
	case code != 0:
	case !stored:
		fmt.Fprintln(out, "no stored credentials")
	default:
		fmt.Fprintln(out, "logged out")
	}
	return code
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
		tea.WithContext(ctx))
	final, err := prog.Run()
	if err != nil && ctx.Err() == nil {
		return err
	}
	if m, ok := final.(ui.Model); ok {
		if m.SignedOut() {
			return errors.New(signedOutHint)
		}
		if notice := m.Denied(); notice != "" {
			return errors.New(notice)
		}
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

// openDebugLog replaces the log at path with a fresh one, creating its
// directory. Mode 0600: frames are redacted but chat text is still the
// user's private conversation. The old file is removed rather than
// truncated because OpenFile applies the mode only to a file it
// creates, so a copy left with a wider mode would keep it; anything at
// the path that is not a regular file (a symlink, a FIFO) is refused
// rather than written through. Windows has no mode bits; the per-user
// state directory's ACL is the boundary there.
func openDebugLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("debug log: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("debug log: %s is not a regular file", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("debug log: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("debug log: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
	remember, err := store.Load(site.CredentialKey())
	if errors.Is(err, auth.ErrKeyringUnavailable) {
		// Whatever the keyring holds cannot be used this run; a fresh
		// login lands in the file, which Load reads first from then on.
		fmt.Fprintln(os.Stderr, "ngchat:", err, "; logging in again")
	}
	if errors.Is(err, auth.ErrNotFound) || errors.Is(err, auth.ErrKeyringUnavailable) {
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
	if err := store.Save(site.CredentialKey(), sess.Remember); err != nil {
		return auth.Session{}, fmt.Errorf("storing credential: %w", err)
	}
	fmt.Printf("logged in as %s\n", render.Line(sess.User.Username))
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
  ngchat logout               log this device out on the site and clear the cookie

flags:
`)
	flag.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
environment:
  NGCHAT_WS_URL           chat WebSocket URL (default %s)
  NGCHAT_SITE_URL         site base URL (default %s)
  NGCHAT_ROUTING_COOKIE   extra cookie for the dev/staging proxy
  NGCHAT_NG_COOKIE        NG cookie header (bypasses the stored login)

Both URLs must be wss/https unless the host is loopback, and the chat
host must be on the site's domain. A login is stored per site, so
switching NGCHAT_SITE_URL never sends one site's cookie to another; log
in once per site.
`, defaultWSURL, defaultSiteURL)
}

// fatal prints an error and exits non-zero.
// fatal prints the error and exits 1. The entry-gate notice carries
// styling from the renderer, which Lip Gloss v2 no longer downsamples
// at render time, so the writer does it for the terminal at hand.
func fatal(err error) {
	_, _ = lipgloss.Fprintln(os.Stderr, "ngchat:", err)
	os.Exit(1)
}
