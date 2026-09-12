// Package run is the one "prepare a run" step shared by cmd/ngchat and
// cmd/smoke: the environment both read, the site client with its
// routing cookie, the credential the jar is seeded with, the chat-URL
// check, and the client.Config that follows. The two binaries used to
// hand-write this sequence and drifted (different variable names, one
// skipping the store migration); keeping it here means a change to the
// order or the messages happens once and is tested once, through
// narrowed interfaces rather than a keyring or a network.
package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
	"github.com/newgrounds-inc/ngchat-cli/internal/client"
)

const (
	// DefaultWSURL and DefaultSiteURL are production, the only endpoints
	// that exist outside Newgrounds; the variables below override them
	// for the internal dev stack.
	DefaultWSURL   = "wss://chat.newgrounds.com/ws"
	DefaultSiteURL = "https://www.newgrounds.com"

	// Channel is the one room a run joins. The server has one channel
	// today and switching is out of scope (docs/roadmap.md).
	Channel = "general"

	// notice prefixes every line written to Options.Notices, so a message
	// from this step reads like the rest of the tool's output.
	notice = "ngchat:"
)

// Env is what a run reads from the environment, held as a value so the
// step can be tested without os.Getenv and so a caller can see, in one
// place, every variable that steers a run.
type Env struct {
	// SiteURL is NGCHAT_SITE_URL, the origin whose cookie jar mints the
	// chat JWT.
	SiteURL string
	// WSURL is NGCHAT_WS_URL, the chat endpoint the JWT is sent to.
	WSURL string
	// Routing is NGCHAT_ROUTING_COOKIE: the dev proxy's routing cookie,
	// which must ride on every request, site and chat alike. Empty in
	// production.
	Routing string
	// CookieHeader is NGCHAT_NG_COOKIE, a raw cookie header that bypasses
	// the stored login. Development and the smoke harness only.
	CookieHeader string
}

// redacted stands in for CookieHeader wherever an Env is printed. The
// header is a bearer for the account, and secrets never touch stdout
// or a log (AGENTS.md), so a stray %v or slog attribute of the whole
// Env must not be the way one leaks.
const redacted = "[redacted]"

// String prints the URLs and routing cookie and hides the cookie
// header, so fmt's %v and %+v of an Env are safe to write anywhere.
// Whether a header is set is still shown, since that is what a debug
// log needs to explain which credential a run used.
func (e Env) String() string {
	header := ""
	if e.CookieHeader != "" {
		header = redacted
	}
	return fmt.Sprintf("{SiteURL:%s WSURL:%s Routing:%s CookieHeader:%s}",
		e.SiteURL, e.WSURL, e.Routing, header)
}

// LogValue gives slog the same redacted view as String, as a group so
// each field is still queryable, with the header's presence kept and
// its value dropped.
func (e Env) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("site", e.SiteURL),
		slog.String("ws", e.WSURL),
		slog.String("routing", e.Routing),
		slog.Bool("cookieHeader", e.CookieHeader != ""),
	)
}

// FromEnv reads Env from the process environment, defaulting the URLs
// to production so a run with nothing set is a run against the real
// site, as README.md promises.
func FromEnv() Env {
	return Env{
		SiteURL:      envOr("NGCHAT_SITE_URL", DefaultSiteURL),
		WSURL:        envOr("NGCHAT_WS_URL", DefaultWSURL),
		Routing:      os.Getenv("NGCHAT_ROUTING_COOKIE"),
		CookieHeader: os.Getenv("NGCHAT_NG_COOKIE"),
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// NewSite builds the site client for the run with the routing cookie
// seeded, so it rides on every request the way it does in a browser.
// It is separate from Prepare because login and logout need the site
// too, and neither must be blocked by a stale chat URL or a missing
// credential. Errors name the variable at fault.
func NewSite(env Env) (*auth.Site, error) {
	site, err := auth.NewSite(env.SiteURL)
	if err != nil {
		return nil, fmt.Errorf("NGCHAT_SITE_URL: %w", err)
	}
	if env.Routing != "" {
		if err := site.SeedCookies(env.Routing); err != nil {
			return nil, fmt.Errorf("NGCHAT_ROUTING_COOKIE: %w", err)
		}
	}
	return site, nil
}

// CredentialStore is what Prepare needs from auth.Store, narrowed so the
// outcome matrix can be scripted without a keyring.
type CredentialStore interface {
	Load(key string) (string, error)
	Migrate() (bool, error)
}

// Site is what Prepare needs from *auth.Site, narrowed so the jar can be
// a fake that records what it was seeded with.
type Site interface {
	CredentialKey() string
	SeedCookies(header string) error
	SetRemember(value string)
	CheckChatURL(raw string) error
}

// Source says where the credential a run minted with came from. The
// smoke harness prints it because an exported NGCHAT_NG_COOKIE silently
// wins over a fresh `ngchat login`, which is the one thing an operator
// verifying the login flow needs to know.
type Source int

const (
	// FromHeader: NGCHAT_NG_COOKIE was set and seeded as-is.
	FromHeader Source = iota + 1
	// FromStore: the remember cookie `ngchat login` persisted.
	FromStore
	// FromLogin: nothing usable was stored and Options.Login ran.
	FromLogin
)

// String names the source for a log line.
func (s Source) String() string {
	switch s {
	case FromHeader:
		return "NGCHAT_NG_COOKIE from the environment"
	case FromStore:
		return "stored login"
	case FromLogin:
		return "fresh login"
	}
	return fmt.Sprintf("Source(%d)", int(s))
}

// ErrNoCredential is returned by Prepare when nothing usable is stored
// and Options.Login is nil. It wraps the store's own answer, so
// errors.Is still tells auth.ErrNotFound (log in) from
// auth.ErrKeyringUnavailable (unlock the keyring, or log in and the
// value lands in the file).
var ErrNoCredential = errors.New(
	"no NGCHAT_NG_COOKIE and no usable stored login")

// Options is what Prepare works with. Store and Site are interfaces
// rather than the auth types so the decision table is tested with
// fakes; Env is a value so no test reads the real environment.
type Options struct {
	Env   Env
	Store CredentialStore
	Site  Site
	// Login runs when the store has nothing usable: not found, or a
	// keyring that gave no answer. It must leave the jar authenticated
	// (auth.Login does), since the first mint follows with no further
	// setup. nil means the caller cannot prompt, and Prepare returns
	// ErrNoCredential instead; that is how the headless smoke harness
	// stays headless (ADR 0008).
	Login func(ctx context.Context) error
	// Notices receives the lines a user should see on the way: a
	// removed v0.1 slot, a keyring that could not be read. nil discards
	// them. Never a secret.
	Notices io.Writer
}

// Prepare gets the jar into a state that can mint, in this order:
//
//  1. The chat URL is checked, before anything is minted, because the
//     token minted from the site's cookie goes to it as the first frame.
//  2. NGCHAT_NG_COOKIE, when set, is seeded as-is and nothing stored is
//     read.
//  3. Otherwise the v0.1 cookie-header slot is removed (it cannot be
//     converted, ADR 0003), the stored remember cookie is loaded, and
//     when there is none Options.Login runs.
//
// A keyring that gave no answer counts as "log in": whatever it holds
// cannot be used this run, and a fresh login lands in the file, which
// the store reads first from then on. Any other store error propagates.
func Prepare(ctx context.Context, o Options) (Source, error) {
	if o.Notices == nil {
		o.Notices = io.Discard
	}
	if err := o.Site.CheckChatURL(o.Env.WSURL); err != nil {
		return 0, fmt.Errorf("NGCHAT_WS_URL: %w", err)
	}
	if o.Env.CookieHeader != "" {
		if err := o.Site.SeedCookies(o.Env.CookieHeader); err != nil {
			return 0, fmt.Errorf("NGCHAT_NG_COOKIE: %w", err)
		}
		return FromHeader, nil
	}
	removed, err := o.Store.Migrate()
	if err != nil {
		return 0, err
	}
	if removed {
		fmt.Fprintln(o.Notices, notice, "the cookie stored by an older "+
			"version was removed; please log in once")
	}
	remember, err := o.Store.Load(o.Site.CredentialKey())
	switch {
	case err == nil:
		o.Site.SetRemember(remember)
		return FromStore, nil
	case !errors.Is(err, auth.ErrNotFound) &&
		!errors.Is(err, auth.ErrKeyringUnavailable):
		return 0, err
	case o.Login == nil:
		return 0, fmt.Errorf("%w: %w", ErrNoCredential, err)
	}
	if errors.Is(err, auth.ErrKeyringUnavailable) {
		fmt.Fprintln(o.Notices, notice, err.Error()+"; logging in again")
	}
	if err := o.Login(ctx); err != nil {
		return 0, err
	}
	return FromLogin, nil
}

// ClientConfig is the client.Config both binaries run: the chat URL and
// routing cookie from Env, the one Channel, and a minter over the site
// Prepare just seeded. log is nil for no logging.
func ClientConfig(env Env, site *auth.Site, log *slog.Logger) client.Config {
	return client.Config{
		WSURL:   env.WSURL,
		Channel: Channel,
		Minter:  &auth.ServiceTokenMinter{Site: site},
		Cookie:  env.Routing,
		Log:     log,
	}
}
