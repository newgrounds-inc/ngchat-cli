package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captured records what the fake jwt.php endpoint received.
type captured struct {
	method      string
	cookie      string
	accept      string
	contentType string
	body        string
}

// fakeJWT serves a canned response and records the request.
func fakeJWT(t *testing.T, status int, body string) (*httptest.Server, *captured) {
	t.Helper()
	got := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			*got = captured{
				method:      r.Method,
				cookie:      r.Header.Get("Cookie"),
				accept:      r.Header.Get("Accept"),
				contentType: r.Header.Get("Content-Type"),
				body:        string(raw),
			}
			w.WriteHeader(status)
			io.WriteString(w, body)
		}))
	t.Cleanup(srv.Close)
	return srv, got
}

const successBody = `{"status":"success","data":{"token":"tok-123"}}`

func TestCookieMinter(t *testing.T) {
	srv, got := fakeJWT(t, 200, successBody)
	m := &CookieMinter{
		JWTURL:   srv.URL,
		NGCookie: "vmkldu5I8m=abc",
		Cookie:   "serverid=dev1",
	}

	token, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if token != "tok-123" {
		t.Errorf("token = %q, want %q", token, "tok-123")
	}
	if got.method != http.MethodGet {
		t.Errorf("method = %s, want GET", got.method)
	}
	// The dev proxy routing cookie rides along with the NG session cookie.
	if got.cookie != "vmkldu5I8m=abc; serverid=dev1" {
		t.Errorf("Cookie = %q", got.cookie)
	}
	if got.accept != "application/json" {
		t.Errorf("Accept = %q, want application/json", got.accept)
	}
}

func TestCookieMinterWithoutRoutingCookie(t *testing.T) {
	srv, got := fakeJWT(t, 200, successBody)
	m := &CookieMinter{JWTURL: srv.URL, NGCookie: "vmkldu5I8m=abc"}
	if _, err := m.Mint(context.Background()); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if got.cookie != "vmkldu5I8m=abc" {
		t.Errorf("Cookie = %q, want no trailing separator", got.cookie)
	}
}

func TestPasswordMinter(t *testing.T) {
	srv, got := fakeJWT(t, 200, successBody)
	m := &PasswordMinter{
		JWTURL:   srv.URL,
		Username: "bob",
		Password: "hunter2",
		Cookie:   "serverid=dev1",
	}

	token, err := m.Mint(context.Background())
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if token != "tok-123" {
		t.Errorf("token = %q", token)
	}
	if got.method != http.MethodPost {
		t.Errorf("method = %s, want POST", got.method)
	}
	if got.contentType != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q", got.contentType)
	}
	if got.body != "password=hunter2&username=bob" {
		t.Errorf("body = %q", got.body)
	}
	if got.cookie != "serverid=dev1" {
		t.Errorf("Cookie = %q", got.cookie)
	}
}

// TestMintErrorShapes covers jwt.php's mixed failure formats: JSend on some
// paths, raw Laravel {message} on others, and neither when something
// upstream fails.
func TestMintErrorShapes(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantText string
	}{
		{"jsend fail", 401,
			`{"status":"fail","message":"invalid credentials"}`,
			"invalid credentials"},
		{"laravel validation", 422,
			`{"message":"The username field is required.","errors":{}}`,
			"The username field is required."},
		{"laravel lockout", 429,
			`{"message":"Too many login attempts."}`,
			"Too many login attempts."},
		{"html error page", 500, `<html>gateway blew up</html>`,
			"Internal Server Error"},
		{"empty body", 503, ``, "Service Unavailable"},
		{"success status but no token", 200,
			`{"status":"success","data":{}}`, "OK"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeJWT(t, tc.status, tc.body)
			m := &CookieMinter{JWTURL: srv.URL, NGCookie: "c=1"}

			token, err := m.Mint(context.Background())
			if err == nil {
				t.Fatalf("expected an error, got token %q", token)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error %q does not mention %q", err, tc.wantText)
			}
		})
	}
}

// TestMintDoesNotLeakSecrets guards the one rule that matters if these
// errors ever reach a log: no cookie or password in the message.
func TestMintDoesNotLeakSecrets(t *testing.T) {
	srv, _ := fakeJWT(t, 401, `{"status":"fail","message":"nope"}`)

	_, err := (&CookieMinter{JWTURL: srv.URL, NGCookie: "secret-cookie"}).
		Mint(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-cookie") {
		t.Errorf("cookie leaked into error: %v", err)
	}

	_, err = (&PasswordMinter{JWTURL: srv.URL, Username: "bob",
		Password: "hunter2"}).Mint(context.Background())
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("password leaked into error: %v", err)
	}
}

func TestMintRespectsContext(t *testing.T) {
	srv, _ := fakeJWT(t, 200, successBody)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (&CookieMinter{JWTURL: srv.URL}).Mint(ctx); err == nil {
		t.Error("expected the canceled context to abort the request")
	}
}
