package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
)

// fakeStore and fakeSite script the Prepare decision table without a
// keyring, a config file or a network. The site fake delegates the
// chat-URL check to a real *auth.Site so the domain rule is the real
// one, and records what the jar was seeded with.
type fakeStore struct {
	loadValue  string
	loadErr    error
	migrated   bool
	migrateErr error
	loads      int
}

func (f *fakeStore) Load(string) (string, error) {
	f.loads++
	return f.loadValue, f.loadErr
}

func (f *fakeStore) Migrate() (bool, error) { return f.migrated, f.migrateErr }

type fakeSite struct {
	real     *auth.Site
	seeded   []string
	remember string
}

func (f *fakeSite) CredentialKey() string         { return f.real.CredentialKey() }
func (f *fakeSite) SetRemember(v string)          { f.remember = v }
func (f *fakeSite) CheckChatURL(raw string) error { return f.real.CheckChatURL(raw) }

func (f *fakeSite) SeedCookies(header string) error {
	if err := f.real.SeedCookies(header); err != nil {
		return err
	}
	f.seeded = append(f.seeded, header)
	return nil
}

func newFakeSite(t *testing.T) *fakeSite {
	t.Helper()
	real, err := auth.NewSite(DefaultSiteURL)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSite{real: real}
}

func TestPrepare(t *testing.T) {
	unavailable := fmt.Errorf("%w: no secret service", auth.ErrKeyringUnavailable)
	corrupt := errors.New("parsing credentials.json: unexpected end")
	loginFailed := errors.New("login: lockout")
	tests := []struct {
		name       string
		env        Env
		store      fakeStore
		login      bool  // install a login fallback
		loginErr   error // what it returns
		wantSource Source
		wantErr    string // substring; "" means success
		wantIs     error  // errors.Is target, nil to skip
		wantLogin  int    // fallback calls
		wantLoads  int
		wantSeeded string // cookie header seeded, "" for none
		wantJar    string // remember value set, "" for none
		wantNotice string // substring of Notices, "" for nothing
	}{
		{
			name:       "env header wins over the store",
			env:        Env{WSURL: DefaultWSURL, CookieHeader: "ng_remember=abc"},
			store:      fakeStore{loadValue: "stored"},
			login:      true,
			wantSource: FromHeader,
			wantSeeded: "ng_remember=abc",
		},
		{
			name:    "bad env header names the variable",
			env:     Env{WSURL: DefaultWSURL, CookieHeader: "not a cookie"},
			login:   true,
			wantErr: "NGCHAT_NG_COOKIE: parsing cookie header",
		},
		{
			name:       "stored login seeds the jar",
			env:        Env{WSURL: DefaultWSURL},
			store:      fakeStore{loadValue: "stored"},
			login:      true,
			wantSource: FromStore,
			wantLoads:  1,
			wantJar:    "stored",
		},
		{
			name:       "migrate removed a v0.1 slot: say so, then log in",
			env:        Env{WSURL: DefaultWSURL},
			store:      fakeStore{migrated: true, loadErr: auth.ErrNotFound},
			login:      true,
			wantSource: FromLogin,
			wantLoads:  1,
			wantLogin:  1,
			wantNotice: "ngchat: the cookie stored by an older version was removed; please log in once",
		},
		{
			name:       "migrate removed a v0.1 slot but a login is stored",
			env:        Env{WSURL: DefaultWSURL},
			store:      fakeStore{migrated: true, loadValue: "stored"},
			login:      true,
			wantSource: FromStore,
			wantLoads:  1,
			wantJar:    "stored",
			wantNotice: "removed; please log in once",
		},
		{
			name:    "migrate failure propagates",
			env:     Env{WSURL: DefaultWSURL},
			store:   fakeStore{migrateErr: corrupt},
			login:   true,
			wantErr: "parsing credentials.json",
			wantIs:  corrupt,
		},
		{
			name:       "not found runs the fallback quietly",
			env:        Env{WSURL: DefaultWSURL},
			store:      fakeStore{loadErr: auth.ErrNotFound},
			login:      true,
			wantSource: FromLogin,
			wantLoads:  1,
			wantLogin:  1,
		},
		{
			name:       "keyring unavailable runs the fallback and says why",
			env:        Env{WSURL: DefaultWSURL},
			store:      fakeStore{loadErr: unavailable},
			login:      true,
			wantSource: FromLogin,
			wantLoads:  1,
			wantLogin:  1,
			wantNotice: "ngchat: OS keyring unavailable: no secret service; logging in again",
		},
		{
			name:      "fallback failure propagates",
			env:       Env{WSURL: DefaultWSURL},
			store:     fakeStore{loadErr: auth.ErrNotFound},
			login:     true,
			loginErr:  loginFailed,
			wantErr:   "lockout",
			wantIs:    loginFailed,
			wantLoads: 1,
			wantLogin: 1,
		},
		{
			name:      "not found without a fallback is ErrNoCredential",
			env:       Env{WSURL: DefaultWSURL},
			store:     fakeStore{loadErr: auth.ErrNotFound},
			wantErr:   "no NGCHAT_NG_COOKIE and no usable stored login: credential not found",
			wantIs:    ErrNoCredential,
			wantLoads: 1,
		},
		{
			name:      "keyring unavailable without a fallback keeps its cause",
			env:       Env{WSURL: DefaultWSURL},
			store:     fakeStore{loadErr: unavailable},
			wantErr:   "no NGCHAT_NG_COOKIE and no usable stored login: OS keyring unavailable: no secret service",
			wantIs:    auth.ErrKeyringUnavailable,
			wantLoads: 1,
		},
		{
			name:      "other load errors propagate, fallback untouched",
			env:       Env{WSURL: DefaultWSURL},
			store:     fakeStore{loadErr: corrupt},
			login:     true,
			wantErr:   "parsing credentials.json",
			wantIs:    corrupt,
			wantLoads: 1,
		},
		{
			name:    "bad chat URL is refused before anything is read",
			env:     Env{WSURL: "wss://chat.example.com/ws", CookieHeader: "ng_remember=abc"},
			store:   fakeStore{loadValue: "stored"},
			login:   true,
			wantErr: "NGCHAT_WS_URL: chat URL \"wss://chat.example.com/ws\" is not on the site's domain",
		},
		{
			name:    "plaintext chat URL is refused",
			env:     Env{WSURL: "ws://chat.newgrounds.com/ws"},
			store:   fakeStore{loadValue: "stored"},
			login:   true,
			wantErr: "NGCHAT_WS_URL: chat URL \"ws://chat.newgrounds.com/ws\": scheme \"ws\"",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, site := tc.store, newFakeSite(t)
			var notices bytes.Buffer
			logins := 0
			opts := Options{Env: tc.env, Store: &store, Site: site, Notices: &notices}
			if tc.login {
				opts.Login = func(context.Context) error {
					logins++
					return tc.loginErr
				}
			}
			src, err := Prepare(context.Background(), opts)

			if tc.wantErr == "" && err != nil {
				t.Fatalf("Prepare error = %v, want none", err)
			}
			if tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("Prepare error = %v, want it to mention %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
			}
			if src != tc.wantSource {
				t.Errorf("source = %v, want %v", src, tc.wantSource)
			}
			if logins != tc.wantLogin {
				t.Errorf("fallback calls = %d, want %d", logins, tc.wantLogin)
			}
			if store.loads != tc.wantLoads {
				t.Errorf("store loads = %d, want %d", store.loads, tc.wantLoads)
			}
			if got := strings.Join(site.seeded, ";"); got != tc.wantSeeded {
				t.Errorf("seeded headers = %q, want %q", got, tc.wantSeeded)
			}
			if site.remember != tc.wantJar {
				t.Errorf("remember set = %q, want %q", site.remember, tc.wantJar)
			}
			if tc.wantNotice == "" && notices.Len() != 0 {
				t.Errorf("notices = %q, want nothing", notices.String())
			}
			if tc.wantNotice != "" && !strings.Contains(notices.String(), tc.wantNotice) {
				t.Errorf("notices = %q, want them to mention %q", notices.String(), tc.wantNotice)
			}
		})
	}
}

// TestPrepareNilNotices: a caller with nowhere to print must still get
// the same decisions.
func TestPrepareNilNotices(t *testing.T) {
	store := fakeStore{migrated: true, loadErr: auth.ErrNotFound}
	src, err := Prepare(context.Background(), Options{
		Env:   Env{WSURL: DefaultWSURL},
		Store: &store,
		Site:  newFakeSite(t),
		Login: func(context.Context) error { return nil },
	})
	if err != nil || src != FromLogin {
		t.Errorf("Prepare = %v, %v; want FromLogin, nil", src, err)
	}
}

func TestFromEnv(t *testing.T) {
	for _, key := range []string{"NGCHAT_SITE_URL", "NGCHAT_WS_URL",
		"NGCHAT_ROUTING_COOKIE", "NGCHAT_NG_COOKIE"} {
		t.Setenv(key, "")
	}
	if got, want := FromEnv(), (Env{SiteURL: DefaultSiteURL, WSURL: DefaultWSURL}); got != want {
		t.Errorf("FromEnv() with nothing set = %+v, want %+v", got, want)
	}

	t.Setenv("NGCHAT_SITE_URL", "https://www.newgrounds-d.com")
	t.Setenv("NGCHAT_WS_URL", "wss://chat.newgrounds-d.com/ws")
	t.Setenv("NGCHAT_ROUTING_COOKIE", "serverid=dev1")
	t.Setenv("NGCHAT_NG_COOKIE", "ng_remember=abc")
	want := Env{
		SiteURL:      "https://www.newgrounds-d.com",
		WSURL:        "wss://chat.newgrounds-d.com/ws",
		Routing:      "serverid=dev1",
		CookieHeader: "ng_remember=abc",
	}
	if got := FromEnv(); got != want {
		t.Errorf("FromEnv() = %+v, want %+v", got, want)
	}
}

func TestNewSite(t *testing.T) {
	tests := []struct {
		name    string
		env     Env
		wantKey string
		wantErr string
	}{
		{"production", Env{SiteURL: DefaultSiteURL}, auth.RememberKey, ""},
		{"dev stack with routing cookie",
			Env{SiteURL: "https://www.newgrounds-d.com", Routing: "serverid=dev1"},
			auth.RememberKey + "@www.newgrounds-d.com", ""},
		{"empty site URL names the variable", Env{}, "",
			"NGCHAT_SITE_URL: invalid site URL \"\""},
		{"plaintext site URL names the variable",
			Env{SiteURL: "http://www.newgrounds.com"}, "",
			"NGCHAT_SITE_URL: site URL \"http://www.newgrounds.com\": scheme \"http\""},
		{"bad routing cookie names the variable",
			Env{SiteURL: DefaultSiteURL, Routing: "no equals sign"}, "",
			"NGCHAT_ROUTING_COOKIE: parsing cookie header"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			site, err := NewSite(tc.env)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("NewSite error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewSite error = %v", err)
			}
			if got := site.CredentialKey(); got != tc.wantKey {
				t.Errorf("CredentialKey = %q, want %q", got, tc.wantKey)
			}
		})
	}
}

func TestClientConfig(t *testing.T) {
	site, err := NewSite(Env{SiteURL: DefaultSiteURL})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	env := Env{WSURL: "wss://chat.newgrounds-d.com/ws", Routing: "serverid=dev1"}
	cfg := ClientConfig(env, site, log)
	if cfg.WSURL != env.WSURL || cfg.Cookie != env.Routing || cfg.Channel != Channel {
		t.Errorf("config = %+v, want ws %q cookie %q channel %q",
			cfg, env.WSURL, env.Routing, Channel)
	}
	minter, ok := cfg.Minter.(*auth.ServiceTokenMinter)
	if !ok || minter.Site != site {
		t.Errorf("minter = %#v, want a ServiceTokenMinter over the run's site", cfg.Minter)
	}
	if cfg.Log != log {
		t.Error("logger was not passed through")
	}
	if ClientConfig(env, site, nil).Log != nil {
		t.Error("a nil logger must stay nil so the client logs nothing")
	}
}

func TestSourceString(t *testing.T) {
	for src, want := range map[Source]string{
		FromHeader: "NGCHAT_NG_COOKIE from the environment",
		FromStore:  "stored login",
		FromLogin:  "fresh login",
		Source(0):  "Source(0)",
	} {
		if got := src.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", int(src), got, want)
		}
	}
}

// TestEnvRedacts: every printed form of an Env must keep the URLs (what
// a bug report needs) and never carry the cookie header (a bearer).
func TestEnvRedacts(t *testing.T) {
	const secret = "ng_remember=s3cr3t-value"
	env := Env{
		SiteURL:      "https://www.newgrounds-d.com",
		WSURL:        "wss://chat.newgrounds-d.com/ws",
		Routing:      "serverid=dev1",
		CookieHeader: secret,
	}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("start", "env", env)
	var loggedJSON bytes.Buffer
	slog.New(slog.NewJSONHandler(&loggedJSON, nil)).Info("start", "env", env)
	forms := []struct {
		name string
		text string
	}{
		{"%v", fmt.Sprintf("%v", env)},
		{"%+v", fmt.Sprintf("%+v", env)},
		{"%s", fmt.Sprintf("%s", env)},
		{"String", env.String()},
		{"slog text", logged.String()},
		{"slog json", loggedJSON.String()},
	}
	for _, f := range forms {
		t.Run(f.name, func(t *testing.T) {
			if strings.Contains(f.text, secret) || strings.Contains(f.text, "s3cr3t") {
				t.Fatalf("%s leaked the cookie header: %q", f.name, f.text)
			}
			for _, keep := range []string{env.SiteURL, env.WSURL, env.Routing} {
				if !strings.Contains(f.text, keep) {
					t.Errorf("%s dropped %q: %q", f.name, keep, f.text)
				}
			}
		})
	}
	if got := env.String(); !strings.Contains(got, "CookieHeader:"+redacted) {
		t.Errorf("String() = %q, want the header marked %s", got, redacted)
	}
	if got := (Env{}).String(); !strings.Contains(got, "CookieHeader:}") {
		t.Errorf("String() of an empty Env = %q, want an empty header shown as absent", got)
	}
	if !strings.Contains(logged.String(), "env.cookieHeader=true") {
		t.Errorf("slog text = %q, want the header's presence kept", logged.String())
	}
}
