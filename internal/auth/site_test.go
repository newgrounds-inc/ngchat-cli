package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeSite models the site contract in docs/site-login-endpoints.md:
// session and CSRF cookies from a guest GET auth/me, strict content
// negotiation, X-XSRF-TOKEN checked against the URL-decoded cookie,
// token rotation on every completed login step, and a remember cookie
// that authenticates service-token on its own. Per-test hooks script the
// login, two-factor, and service-token outcomes.
type fakeSite struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	xsrf      string // raw token; the cookie carries it URL-encoded
	rotated   int
	authed    bool
	challenge string
	remember  string
	calls     map[string]int
	bodies    map[string][]map[string]any
	// xsrfSeen records the X-XSRF-TOKEN header of each POST.
	xsrfSeen []string

	// login answers POST login once the guards pass. Default: success
	// without two-factor.
	login func(body map[string]any) (int, string)
	// twoFactor answers POST two-factor once a challenge exists.
	// Default: any "code" succeeds.
	twoFactor func(body map[string]any) (int, string)
	// token answers POST service-token once authenticated. Default: a
	// numbered token.
	token func(n int) (int, string)
	// forbidLoginOnce makes the first login answer 403 as an
	// already-authenticated jar would, regardless of state.
	forbidLoginOnce bool
	// rotateSilently rotates the CSRF token before the next POST without
	// telling the client, forcing a 419.
	rotateSilently bool
}

const fakeRemember = "remember-value-1"

func newFakeSite(t *testing.T) *fakeSite {
	t.Helper()
	fs := &fakeSite{
		t:        t,
		xsrf:     "xsrf-0==",
		remember: fakeRemember,
		calls:    map[string]int{},
		bodies:   map[string][]map[string]any{},
	}
	fs.srv = httptest.NewServer(http.HandlerFunc(fs.handle))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeSite) site(t *testing.T) *Site {
	t.Helper()
	s, err := NewSite(fs.srv.URL)
	if err != nil {
		t.Fatalf("NewSite: %v", err)
	}
	return s
}

func (fs *fakeSite) rotate() {
	fs.rotated++
	fs.xsrf = fmt.Sprintf("xsrf-%d==", fs.rotated)
}

func (fs *fakeSite) setSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: xsrfCookie,
		Value: url.QueryEscape(fs.xsrf), Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "ng_session", Value: "sess",
		Path: "/"})
}

func (fs *fakeSite) completeLogin(w http.ResponseWriter) {
	fs.authed = true
	fs.challenge = ""
	fs.rotate()
	fs.setSessionCookies(w)
	http.SetCookie(w, &http.Cookie{Name: rememberCookie,
		Value: fs.remember, Path: "/"})
}

func write(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func jsendFail(field, msg string) string {
	raw, _ := json.Marshal(map[string]any{"status": "fail",
		"data": map[string][]string{field: {msg}}})
	return string(raw)
}

func (fs *fakeSite) handle(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.calls[r.URL.Path]++

	if r.Header.Get("Accept") != "application/json" {
		write(w, 406, jsendFail("http", "Not Acceptable"))
		return
	}
	var body map[string]any
	if r.Method == http.MethodPost {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			write(w, 415, jsendFail("http", "Unsupported Media Type"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			write(w, 400, jsendFail("http", "bad json"))
			return
		}
		fs.bodies[r.URL.Path] = append(fs.bodies[r.URL.Path], body)
		if fs.rotateSilently {
			fs.rotateSilently = false
			fs.rotate()
		}
		header := r.Header.Get("X-XSRF-TOKEN")
		fs.xsrfSeen = append(fs.xsrfSeen, header)
		_, hasSession := cookieValue(r, "ng_session")
		if header != fs.xsrf || !hasSession {
			// Laravel's CSRF middleware answers before the JSend layer.
			write(w, 419, `{"message":"CSRF token mismatch."}`)
			return
		}
	}

	remembered := false
	if v, ok := cookieValue(r, rememberCookie); ok && v == fs.remember {
		remembered = true
	}

	switch r.URL.Path {
	case mePath:
		fs.setSessionCookies(w)
		if fs.authed || remembered {
			write(w, 200, `{"status":"success","data":{"id":7,"username":"bob"}}`)
			return
		}
		write(w, 401, jsendFail("auth", "Unauthenticated."))
	case loginPath:
		if fs.forbidLoginOnce {
			fs.forbidLoginOnce = false
			write(w, 403, jsendFail("http", "This action is unauthorized."))
			return
		}
		if fs.authed {
			write(w, 403, jsendFail("http", "This action is unauthorized."))
			return
		}
		status, resp := 200, `{"status":"success","data":{"two_factor":null,"user":{"id":7,"username":"bob"}}}`
		if fs.login != nil {
			status, resp = fs.login(body)
		}
		if status == 200 {
			var env struct {
				Data struct {
					TwoFactor *string `json:"two_factor"`
				} `json:"data"`
			}
			_ = json.Unmarshal([]byte(resp), &env)
			if env.Data.TwoFactor == nil {
				fs.completeLogin(w)
			} else {
				fs.challenge = *env.Data.TwoFactor
			}
		}
		write(w, status, resp)
	case twoFactorPath:
		if fs.challenge == "" {
			write(w, 403, jsendFail("http", "This action is unauthorized."))
			return
		}
		status, resp := 200, `{"status":"success","data":{"user":{"id":7,"username":"bob"}}}`
		if fs.twoFactor != nil {
			status, resp = fs.twoFactor(body)
		}
		if status == 200 {
			fs.completeLogin(w)
		}
		write(w, status, resp)
	case serviceTokenPath:
		if !fs.authed && !remembered {
			write(w, 401, jsendFail("auth", "Unauthenticated."))
			return
		}
		n := fs.calls[serviceTokenPath]
		status, resp := 200, fmt.Sprintf(
			`{"status":"success","data":{"service":"chat","token":"tok-%d","expires_in":3600}}`, n)
		if fs.token != nil {
			status, resp = fs.token(n)
		}
		write(w, status, resp)
	default:
		write(w, 404, jsendFail("http", "Not Found"))
	}
}

func cookieValue(r *http.Request, name string) (string, bool) {
	c, err := r.Cookie(name)
	if err != nil {
		return "", false
	}
	return c.Value, true
}

func (fs *fakeSite) count(path string) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.calls[path]
}

func (fs *fakeSite) lastBody(path string) map[string]any {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	b := fs.bodies[path]
	if len(b) == 0 {
		return nil
	}
	return b[len(b)-1]
}

func TestPrimeSetsSessionAndCSRFCookies(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	if err := s.Prime(context.Background()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if got := s.xsrfToken(); got != "xsrf-0==" {
		t.Errorf("xsrf token = %q, want the URL-decoded cookie value", got)
	}
	if fs.count(mePath) != 1 {
		t.Errorf("auth/me calls = %d, want 1", fs.count(mePath))
	}
}

func TestPrimeWithoutCSRFCookieFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			write(w, 401, jsendFail("auth", "Unauthenticated."))
		}))
	t.Cleanup(srv.Close)
	s, _ := NewSite(srv.URL)
	if err := s.Prime(context.Background()); err == nil {
		t.Error("expected an error when the site sets no XSRF-TOKEN cookie")
	}
}

func TestLoginWithoutTwoFactor(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	res, err := s.Login(context.Background(), "bob", "hunter2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.TwoFactor != "" || res.User.Username != "bob" {
		t.Errorf("result = %+v", res)
	}
	body := fs.lastBody(loginPath)
	if body["identity"] != "bob" || body["password"] != "hunter2" ||
		body["remember"] != true {
		t.Errorf("login body = %v, want identity/password/remember=true", body)
	}
	if v, ok := s.Remember(); !ok || v != fakeRemember {
		t.Errorf("remember cookie in jar = %q, %v", v, ok)
	}
}

// TestLoginPrimesLazily: the first POST of a run finds no CSRF token in
// the jar and primes on its own, costing one auth/me hit.
func TestLoginPrimesLazily(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	if _, err := s.Login(context.Background(), "bob", "hunter2"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if fs.count(mePath) != 1 {
		t.Errorf("auth/me calls = %d, want exactly 1", fs.count(mePath))
	}
}

func TestLoginReportsTwoFactorMethods(t *testing.T) {
	tests := []struct {
		name string
		data string
		want LoginResult
	}{
		{"email", `{"two_factor":"email","obfuscated_email":"b***@example.com"}`,
			LoginResult{TwoFactor: "email", ObfuscatedEmail: "b***@example.com"}},
		{"totp", `{"two_factor":"totp","obfuscated_email":null}`,
			LoginResult{TwoFactor: "totp"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeSite(t)
			fs.login = func(map[string]any) (int, string) {
				return 200, `{"status":"success","data":` + tc.data + `}`
			}
			res, err := fs.site(t).Login(context.Background(), "bob", "pw")
			if err != nil {
				t.Fatalf("Login: %v", err)
			}
			if res != tc.want {
				t.Errorf("result = %+v, want %+v", res, tc.want)
			}
		})
	}
}

// TestLoginFailures checks that every documented fail outcome surfaces
// the site's message and status untouched.
func TestLoginFailures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantMsg    string
		wantField  string
	}{
		{"bad credentials", 422,
			jsendFail("identity", "These credentials do not match our records."),
			422, "These credentials do not match our records.", "identity"},
		{"password rule", 422, jsendFail("password", "The password field is required."),
			422, "The password field is required.", "password"},
		{"undeliverable", 422,
			jsendFail("undeliverable", "We cannot deliver a code to your email address."),
			422, "We cannot deliver a code to your email address.", "undeliverable"},
		{"lockout", 429,
			jsendFail("identity", "Too many login attempts. Please try again in 57 seconds."),
			429, "Too many login attempts. Please try again in 57 seconds.", "identity"},
		{"jsend error", 500, `{"status":"error","message":"boom","code":500}`,
			500, "boom", "error"},
		{"html page", 502, `<html>bad gateway</html>`, 502, "Bad Gateway", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeSite(t)
			fs.login = func(map[string]any) (int, string) {
				return tc.status, tc.body
			}
			_, err := fs.site(t).Login(context.Background(), "bob", "pw")
			var fail *FailError
			if !errors.As(err, &fail) {
				t.Fatalf("err = %v, want *FailError", err)
			}
			if fail.Status != tc.wantStatus {
				t.Errorf("status = %d, want %d", fail.Status, tc.wantStatus)
			}
			if fail.Message() != tc.wantMsg {
				t.Errorf("message = %q, want %q", fail.Message(), tc.wantMsg)
			}
			if tc.wantField != "" && !fail.Has(tc.wantField) {
				t.Errorf("Has(%q) = false", tc.wantField)
			}
		})
	}
}

// TestLoginForbiddenResetsJar: a 403 means the jar is already
// authenticated, which a fresh jar cannot be, so it is cleared,
// re-primed, and the login retried exactly once.
func TestLoginForbiddenResetsJar(t *testing.T) {
	fs := newFakeSite(t)
	fs.forbidLoginOnce = true
	s := fs.site(t)
	if err := s.SeedCookies("serverid=dev1"); err != nil {
		t.Fatalf("SeedCookies: %v", err)
	}
	if _, err := s.Login(context.Background(), "bob", "pw"); err != nil {
		t.Fatalf("Login after 403: %v", err)
	}
	if fs.count(loginPath) != 2 || fs.count(mePath) != 2 {
		t.Errorf("login calls = %d, me calls = %d, want 2 and 2",
			fs.count(loginPath), fs.count(mePath))
	}
	// The routing cookie must survive the reset.
	found := false
	for _, c := range s.jar.Cookies(s.base) {
		if c.Name == "serverid" && c.Value == "dev1" {
			found = true
		}
	}
	if !found {
		t.Error("seeded routing cookie lost on jar reset")
	}
}

func TestTwoFactorSplitsCodeAndRecoveryCode(t *testing.T) {
	tests := []struct {
		input string
		field string
	}{
		{"123456", "code"},
		{"abcdef", "code"},
		{"abcd-efgh", "recovery_code"},
		{"12345", "recovery_code"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			fs := newFakeSite(t)
			fs.login = func(map[string]any) (int, string) {
				return 200, `{"status":"success","data":{"two_factor":"totp","obfuscated_email":null}}`
			}
			s := fs.site(t)
			if _, err := s.Login(context.Background(), "bob", "pw"); err != nil {
				t.Fatalf("Login: %v", err)
			}
			user, err := s.TwoFactor(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("TwoFactor: %v", err)
			}
			if user.Username != "bob" {
				t.Errorf("user = %+v", user)
			}
			body := fs.lastBody(twoFactorPath)
			if body[tc.field] != tc.input || len(body) != 1 {
				t.Errorf("body = %v, want only %q", body, tc.field)
			}
		})
	}
}

func TestTwoFactorWithoutChallengeIsErrNoChallenge(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	_, err := s.TwoFactor(context.Background(), "123456")
	if !errors.Is(err, ErrNoChallenge) {
		t.Errorf("err = %v, want ErrNoChallenge", err)
	}
}

// TestXSRFRotatesBetweenSteps is the reason the token is read from the
// jar before every POST: login success rotates it, and a cached value
// would 419 on service-token.
func TestXSRFRotatesBetweenSteps(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	ctx := context.Background()
	if _, err := s.Login(ctx, "bob", "pw"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := s.ServiceToken(ctx, ChatService); err != nil {
		t.Fatalf("ServiceToken after login: %v", err)
	}
	fs.mu.Lock()
	seen := append([]string(nil), fs.xsrfSeen...)
	fs.mu.Unlock()
	if len(seen) != 2 || seen[0] != "xsrf-0==" || seen[1] != "xsrf-1==" {
		t.Errorf("X-XSRF-TOKEN per POST = %q, want the rotated value on the second", seen)
	}
	if fs.count(mePath) != 1 {
		t.Errorf("auth/me calls = %d, want 1 (no 419 re-prime needed)", fs.count(mePath))
	}
}

func TestServiceTokenWithRememberCookie(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	s.SetRemember(fakeRemember)
	token, err := s.ServiceToken(context.Background(), ChatService)
	if err != nil {
		t.Fatalf("ServiceToken: %v", err)
	}
	if token != "tok-1" {
		t.Errorf("token = %q", token)
	}
	if body := fs.lastBody(serviceTokenPath); body["service"] != "chat" {
		t.Errorf("body = %v, want service=chat", body)
	}
}

func TestServiceTokenUnauthorizedIsSignedOut(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	s.SetRemember("revoked-value")
	_, err := s.ServiceToken(context.Background(), ChatService)
	if !errors.Is(err, ErrSignedOut) {
		t.Errorf("err = %v, want ErrSignedOut", err)
	}
	if fs.count(serviceTokenPath) != 1 {
		t.Errorf("service-token calls = %d, want 1 (never retry a 401)",
			fs.count(serviceTokenPath))
	}
}

// TestServiceTokenCSRFMismatchRetriesOnce: a 419 re-primes and retries
// exactly once, whether or not the retry succeeds.
func TestServiceTokenCSRFMismatchRetriesOnce(t *testing.T) {
	t.Run("recovers", func(t *testing.T) {
		fs := newFakeSite(t)
		s := fs.site(t)
		s.SetRemember(fakeRemember)
		if err := s.Prime(context.Background()); err != nil {
			t.Fatalf("Prime: %v", err)
		}
		fs.mu.Lock()
		fs.rotateSilently = true
		fs.mu.Unlock()
		token, err := s.ServiceToken(context.Background(), ChatService)
		if err != nil {
			t.Fatalf("ServiceToken: %v", err)
		}
		if token == "" {
			t.Error("empty token")
		}
		if fs.count(serviceTokenPath) != 2 || fs.count(mePath) != 2 {
			t.Errorf("service-token calls = %d, me calls = %d, want 2 and 2",
				fs.count(serviceTokenPath), fs.count(mePath))
		}
	})
	t.Run("gives up", func(t *testing.T) {
		fs := newFakeSite(t)
		fs.token = func(int) (int, string) {
			return 419, `{"message":"CSRF token mismatch."}`
		}
		s := fs.site(t)
		s.SetRemember(fakeRemember)
		_, err := s.ServiceToken(context.Background(), ChatService)
		var fail *FailError
		if !errors.As(err, &fail) || fail.Status != 419 {
			t.Fatalf("err = %v, want a 419 FailError", err)
		}
		if fs.count(serviceTokenPath) != 2 {
			t.Errorf("service-token calls = %d, want 2 (one retry)",
				fs.count(serviceTokenPath))
		}
	})
}

func TestServiceTokenSuccessWithoutToken(t *testing.T) {
	fs := newFakeSite(t)
	fs.token = func(int) (int, string) {
		return 200, `{"status":"success","data":{"service":"chat"}}`
	}
	s := fs.site(t)
	s.SetRemember(fakeRemember)
	if _, err := s.ServiceToken(context.Background(), ChatService); err == nil {
		t.Error("expected an error for a success body with no token")
	}
}

// TestErrorsDoNotLeakSecrets: no password or cookie value may appear in
// an error that could reach a log.
func TestErrorsDoNotLeakSecrets(t *testing.T) {
	fs := newFakeSite(t)
	fs.login = func(map[string]any) (int, string) {
		return 422, jsendFail("identity", "nope")
	}
	s := fs.site(t)
	s.SetRemember("secret-remember")
	_, err := s.Login(context.Background(), "bob", "secret-password")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"secret-password", "secret-remember"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

// TestNonJSONSuccessIsNamed: a 2xx that is not JSend (a proxy page, a
// followed redirect) must not read as "OK" or as a site fail.
func TestNonJSONSuccessIsNamed(t *testing.T) {
	fs := newFakeSite(t)
	fs.token = func(int) (int, string) { return 200, "<html>proxy</html>" }
	s := fs.site(t)
	s.SetRemember(fakeRemember)
	_, err := s.ServiceToken(context.Background(), ChatService)
	if err == nil {
		t.Fatal("expected an error")
	}
	var fail *FailError
	if errors.As(err, &fail) {
		t.Errorf("err is a FailError (%v); a non-JSON 2xx is not a site fail", err)
	}
	if !strings.Contains(err.Error(), "non-JSON") || strings.HasSuffix(err.Error(), "OK") {
		t.Errorf("err = %q, want it to name the unexpected body", err)
	}
}

func TestSeedCookiesRejectsGarbage(t *testing.T) {
	s, _ := NewSite("https://www.example.com")
	if err := s.SeedCookies("not a cookie header"); err == nil {
		t.Error("expected a parse error")
	}
}

func TestNewSiteRejectsBadURL(t *testing.T) {
	for _, u := range []string{"", "www.example.com", "://x"} {
		if _, err := NewSite(u); err == nil {
			t.Errorf("NewSite(%q) accepted", u)
		}
	}
}

func TestSiteRespectsContext(t *testing.T) {
	fs := newFakeSite(t)
	s := fs.site(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ServiceToken(ctx, ChatService); err == nil {
		t.Error("expected the canceled context to abort the request")
	}
}

func TestFailErrorMessagePreference(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string][]string
		status int
		want   string
	}{
		{"known field wins over unknown",
			map[string][]string{"zzz": {"other"}, "password": {"bad"}}, 422, "bad"},
		{"known fields in order",
			map[string][]string{"password": {"pw"}, "identity": {"id"}}, 422, "id"},
		{"unknown fields alphabetical",
			map[string][]string{"beta": {"b"}, "alpha": {"a"}}, 422, "a"},
		{"empty falls back to status text",
			map[string][]string{}, 503, "Service Unavailable"},
		{"empty list skipped",
			map[string][]string{"identity": {}, "code": {"c"}}, 422, "c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &FailError{Status: tc.status, Fields: tc.fields}
			if got := e.Message(); got != tc.want {
				t.Errorf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
}
