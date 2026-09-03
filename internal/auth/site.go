package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/newgrounds-inc/ngchat-cli/internal/render"
)

// Routes under the site's /api/v1/auth group (docs/site-login-endpoints.md).
const (
	mePath           = "/api/v1/auth/me"
	loginPath        = "/api/v1/auth/login"
	logoutPath       = "/api/v1/auth/logout"
	twoFactorPath    = "/api/v1/auth/two-factor"
	serviceTokenPath = "/api/v1/auth/service-token"

	// productionHost is the origin whose credential lives under the bare
	// RememberKey; every other site gets a slot of its own.
	productionHost = "www.newgrounds.com"

	// rememberCookie is the 400-day cookie the site sets on a
	// remember=true login. Its value is the only credential persisted.
	rememberCookie = "ng_remember"
	// xsrfCookie holds the CSRF token the site expects echoed back in
	// X-XSRF-TOKEN on every POST. The site rotates it whenever a login
	// step completes, so it is read from the jar right before each POST.
	xsrfCookie = "XSRF-TOKEN"

	// ChatService is the service name the chat JWT is minted for.
	ChatService = "chat"

	maxBody = 1 << 20
)

// ErrSignedOut reports a 401 from service-token: the remember cookie no
// longer works (password changed, or `ngchat logout` ran). Nothing the
// client can do fixes it, so callers stop instead of retrying.
var ErrSignedOut = errors.New("signed out")

// ErrNoChallenge reports a 403 from two-factor: the login challenge in
// this session expired or was consumed, so login must start over.
var ErrNoChallenge = errors.New("no two-factor challenge in progress")

// FailError is a JSend "fail" (or non-JSend error) response. Fields
// follows Laravel's validation shape: field name to a list of messages.
type FailError struct {
	Status int
	Fields map[string][]string
}

// knownFields is the order the CLI prefers when picking the message to
// show; anything else falls back to the alphabetically first key.
var knownFields = []string{
	"identity", "password", "code", "recovery_code",
	"undeliverable", "auth", "http",
}

// Message returns the first message of the first known field, else the
// first message of any field, else the HTTP status text. The text is
// the site's and goes straight to the terminal, so it is sanitized here
// (render.Line) rather than at every print site.
func (e *FailError) Message() string {
	for _, k := range knownFields {
		if msgs := e.Fields[k]; len(msgs) > 0 {
			return render.Line(msgs[0])
		}
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if msgs := e.Fields[k]; len(msgs) > 0 {
			return render.Line(msgs[0])
		}
	}
	return http.StatusText(e.Status)
}

// Has reports whether the response carried a message for field.
func (e *FailError) Has(field string) bool { return len(e.Fields[field]) > 0 }

// Error implements error with the user-facing message only; the status
// stays available for callers that branch on it.
func (e *FailError) Error() string { return e.Message() }

// User identifies the account that logged in.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// LoginResult is the outcome of a successful password step. TwoFactor is
// "" when the login completed, or "email"/"totp" when a code is needed.
type LoginResult struct {
	TwoFactor       string
	ObfuscatedEmail string
	User            User
}

// Site talks to the newgrounds.com JSON auth API through one in-memory
// cookie jar per run. Only the remember cookie survives the process; the
// session and CSRF cookies are re-established by priming (ADR 0003).
type Site struct {
	base  *url.URL
	http  *http.Client
	jar   http.CookieJar
	seeds []*http.Cookie
}

// NewSite builds a Site for baseURL (e.g. https://www.newgrounds.com).
// The scheme must be https unless the host is loopback: the remember
// cookie and, at login, the password ride on every request, and a
// plaintext origin would hand them to the network.
func NewSite(baseURL string) (*Site, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid site URL %q", baseURL)
	}
	if err := RequireTLS(base, "https"); err != nil {
		return nil, fmt.Errorf("site URL %q: %w", baseURL, err)
	}
	base.Path = ""
	base.Host = strings.ToLower(base.Host)
	s := &Site{base: base}
	s.resetJar()
	return s, nil
}

// RequireTLS rejects u unless its scheme is secure (the https of an
// http/https pair, the wss of a ws/wss pair) or its host is loopback,
// where plaintext is the local dev stack talking to itself.
func RequireTLS(u *url.URL, secure string) error {
	scheme := strings.ToLower(u.Scheme)
	if scheme == secure || IsLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("scheme %q is not allowed off loopback; use %s", u.Scheme, secure)
}

// IsLoopback reports whether host names the local machine: "localhost"
// or a loopback IP literal.
func IsLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// CheckChatURL rejects a chat endpoint the site's token must not go to:
// the scheme must be wss (or the host loopback), and the chat host must
// belong to the same registrable domain as the site (chat.newgrounds.com
// for www.newgrounds.com), a loopback site pairing only with a loopback
// chat host and an IP-literal site only with the same literal. The chat
// JWT is minted from the site's cookie and sent as the first frame, so
// this runs before any mint.
func (s *Site) CheckChatURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid chat URL %q", raw)
	}
	if err := RequireTLS(u, "wss"); err != nil {
		return fmt.Errorf("chat URL %q: %w", raw, err)
	}
	siteHost, chatHost := s.base.Hostname(), strings.ToLower(u.Hostname())
	switch {
	case IsLoopback(siteHost):
		if IsLoopback(chatHost) {
			return nil
		}
	case net.ParseIP(siteHost) != nil:
		if chatHost == siteHost {
			return nil
		}
	default:
		siteDomain, err1 := publicsuffix.EffectiveTLDPlusOne(siteHost)
		chatDomain, err2 := publicsuffix.EffectiveTLDPlusOne(chatHost)
		if err1 == nil && err2 == nil && siteDomain == chatDomain {
			return nil
		}
	}
	return fmt.Errorf("chat URL %q is not on the site's domain (%s)", raw, siteHost)
}

// CredentialKey names the credential-store slot for this site: the bare
// RememberKey for production, "ng_remember@<host>" for anything else.
// A remember cookie is a bearer for the origin that issued it, so a
// dev or staging login never overwrites the production one and, more
// to the point, the production one is never sent anywhere but
// production when NGCHAT_SITE_URL points elsewhere (ADR 0004).
func (s *Site) CredentialKey() string {
	if s.base.Host == productionHost {
		return RememberKey
	}
	return RememberKey + "@" + s.base.Host
}

// resetJar starts a fresh jar, re-applying any seeded cookies so the dev
// proxy routing cookie survives the reset a 403 on login calls for.
func (s *Site) resetJar() {
	jar, _ := cookiejar.New(&cookiejar.Options{
		PublicSuffixList: publicsuffix.List})
	s.jar = jar
	s.http = &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		// The API never redirects, and following one would carry the
		// jar (cookie scope ignores the port) and, on a 307, the login
		// body to wherever it points. A redirect is returned as the
		// non-2xx response it is and surfaces as a FailError.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if len(s.seeds) > 0 {
		s.jar.SetCookies(s.base, s.seeds)
	}
}

// SeedCookies adds a raw Cookie header ("a=1; b=2") to the jar for the
// site host. Used for NGCHAT_NG_COOKIE and the dev proxy routing cookie.
func (s *Site) SeedCookies(header string) error {
	cookies, err := http.ParseCookie(header)
	if err != nil {
		return fmt.Errorf("parsing cookie header: %w", err)
	}
	s.seeds = append(s.seeds, cookies...)
	s.jar.SetCookies(s.base, cookies)
	return nil
}

// SetRemember seeds the jar with a stored remember cookie so the first
// service-token call of a run authenticates. On an https site the cookie
// is marked Secure, so a redirect to a plaintext URL cannot carry it.
func (s *Site) SetRemember(value string) {
	s.jar.SetCookies(s.base, []*http.Cookie{
		{Name: rememberCookie, Value: value, Path: "/",
			Secure: s.base.Scheme == "https"}})
}

// Logout revokes this device's remember token on the site (the token
// rows behind the jar's cookies are deleted, so a copy kept elsewhere
// stops minting) and ends the session. A 401 means the jar is already a
// guest, which is the state being asked for, so it is not an error.
func (s *Site) Logout(ctx context.Context) error {
	err := s.post(ctx, logoutPath, map[string]any{}, nil)
	var fail *FailError
	if errors.As(err, &fail) && fail.Status == http.StatusUnauthorized {
		return nil
	}
	return err
}

// Remember returns the remember cookie currently in the jar, if any.
func (s *Site) Remember() (string, bool) {
	return s.cookie(rememberCookie)
}

func (s *Site) cookie(name string) (string, bool) {
	for _, c := range s.jar.Cookies(s.base) {
		if c.Name == name {
			return c.Value, true
		}
	}
	return "", false
}

// xsrfToken returns the URL-decoded CSRF token from the jar; the site
// sets the cookie URL-encoded but compares the header against the raw
// value.
func (s *Site) xsrfToken() string {
	raw, ok := s.cookie(xsrfCookie)
	if !ok {
		return ""
	}
	v, err := url.QueryUnescape(raw)
	if err != nil {
		return raw
	}
	return v
}

// Prime establishes the session and CSRF cookies with a guest GET of
// auth/me. The 401 it answers is expected; only the cookies matter.
func (s *Site) Prime(ctx context.Context) error {
	err := s.do(ctx, http.MethodGet, mePath, nil, nil)
	var fail *FailError
	if err != nil && !(errors.As(err, &fail) && fail.Status == http.StatusUnauthorized) {
		return fmt.Errorf("priming session: %w", err)
	}
	if _, ok := s.cookie(xsrfCookie); !ok {
		return errors.New("priming session: site set no XSRF-TOKEN cookie")
	}
	return nil
}

// Login runs the password step. A 403 means the jar was already
// authenticated, which a fresh jar cannot be, so it is reset and the
// login retried once.
func (s *Site) Login(ctx context.Context, identity, password string) (LoginResult, error) {
	body := map[string]any{
		"identity": identity,
		"password": password,
		"remember": true,
	}
	var data struct {
		TwoFactor       *string `json:"two_factor"`
		ObfuscatedEmail *string `json:"obfuscated_email"`
		User            *User   `json:"user"`
	}
	err := s.post(ctx, loginPath, body, &data)
	var fail *FailError
	if errors.As(err, &fail) && fail.Status == http.StatusForbidden {
		s.resetJar()
		err = s.post(ctx, loginPath, body, &data)
	}
	if err != nil {
		return LoginResult{}, err
	}
	res := LoginResult{}
	if data.TwoFactor != nil {
		res.TwoFactor = *data.TwoFactor
	}
	if data.ObfuscatedEmail != nil {
		res.ObfuscatedEmail = *data.ObfuscatedEmail
	}
	if data.User != nil {
		res.User = *data.User
	}
	return res, nil
}

// TwoFactor completes the challenge started by Login. A six-character
// entry is sent as the emailed or TOTP code, anything else as a TOTP
// recovery code, so backup codes need no separate command.
func (s *Site) TwoFactor(ctx context.Context, code string) (User, error) {
	body := map[string]string{"recovery_code": code}
	if len([]rune(code)) == 6 {
		body = map[string]string{"code": code}
	}
	var data struct {
		User User `json:"user"`
	}
	err := s.post(ctx, twoFactorPath, body, &data)
	var fail *FailError
	if errors.As(err, &fail) && fail.Status == http.StatusForbidden {
		return User{}, fmt.Errorf("%w: %v", ErrNoChallenge, fail)
	}
	if err != nil {
		return User{}, err
	}
	return data.User, nil
}

// ServiceToken mints a JWT for service. Callers own the rate: the site's
// limiter counts before auth and CSRF, so a retry loop here would also
// lock the user's browser out. A 401 is ErrSignedOut.
func (s *Site) ServiceToken(ctx context.Context, service string) (string, error) {
	var data struct {
		Token string `json:"token"`
	}
	err := s.post(ctx, serviceTokenPath, map[string]string{"service": service}, &data)
	var fail *FailError
	if errors.As(err, &fail) && fail.Status == http.StatusUnauthorized {
		return "", ErrSignedOut
	}
	if err != nil {
		return "", err
	}
	if data.Token == "" {
		return "", errors.New("service-token: success without a token")
	}
	return data.Token, nil
}

// post sends a JSON POST with the current CSRF token, priming first when
// the jar has none and re-priming once on a 419 mismatch.
func (s *Site) post(ctx context.Context, path string, body, out any) error {
	if s.xsrfToken() == "" {
		if err := s.Prime(ctx); err != nil {
			return err
		}
	}
	err := s.do(ctx, http.MethodPost, path, body, out)
	var fail *FailError
	if errors.As(err, &fail) && fail.Status == 419 {
		if err := s.Prime(ctx); err != nil {
			return err
		}
		err = s.do(ctx, http.MethodPost, path, body, out)
	}
	return err
}

// jsend is the site's response envelope. Data is left raw because its
// shape differs between success (an object) and fail (a field map).
type jsend struct {
	Status  string          `json:"status"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// do executes one request and decodes the envelope. Any non-success
// outcome becomes a *FailError so callers branch on Status, including
// non-JSend bodies such as Laravel's plain {"message"} on 419.
func (s *Site) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method,
		s.base.String()+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-XSRF-TOKEN", s.xsrfToken())
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}

	var env jsend
	_ = json.Unmarshal(raw, &env)
	if resp.StatusCode/100 == 2 {
		if env.Status != "success" {
			// A 2xx that is not JSend is not the API answering: a proxy
			// page, or a redirect the client followed. Naming it beats
			// reporting "OK" as a failure.
			return fmt.Errorf("%s %s: unexpected non-JSON response (HTTP %d)",
				method, path, resp.StatusCode)
		}
		if out != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return fmt.Errorf("%s %s: decoding data: %w", method, path, err)
			}
		}
		return nil
	}
	return &FailError{Status: resp.StatusCode, Fields: failFields(env)}
}

// failFields normalizes the three failure shapes into one field map:
// JSend fail (a map of field to messages), JSend error (a bare
// message), and non-JSend Laravel errors ({"message": ...}).
func failFields(env jsend) map[string][]string {
	fields := map[string][]string{}
	if env.Status == "fail" && len(env.Data) > 0 {
		if json.Unmarshal(env.Data, &fields) == nil {
			return fields
		}
		var loose map[string]any
		if json.Unmarshal(env.Data, &loose) == nil {
			for k, v := range loose {
				fields[k] = []string{fmt.Sprint(v)}
			}
			return fields
		}
	}
	if env.Message != "" {
		key := "http"
		if env.Status == "error" {
			key = "error"
		}
		fields[key] = []string{env.Message}
	}
	return fields
}
