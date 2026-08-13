package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Minter obtains a fresh chat JWT. The token lives 1 hour and the chat
// server hard-closes the socket when it lapses, so minting recurs for the
// life of a session.
type Minter interface {
	Mint(ctx context.Context) (string, error)
}

// jsend is the success envelope jwt.php responds with.
type jsend struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Token string `json:"token"`
	} `json:"data"`
}

// laravelError is the non-JSend shape jwt.php failure paths currently use.
type laravelError struct {
	Message string `json:"message"`
}

// PasswordMinter mints via POST jwt.php with username/password. Interim
// path: the site restricts it to ngapps.allowed_users until the JSON
// login+2FA endpoints ship (ADR 0001). The password is held in memory
// only and never persisted.
type PasswordMinter struct {
	JWTURL   string
	Username string
	Password string
	// Cookie is an extra Cookie header, e.g. the dev proxy routing
	// cookie ("serverid=..."), required on every request against the
	// staging/dev stack.
	Cookie string
	HTTP   *http.Client
}

// Mint implements Minter.
func (m *PasswordMinter) Mint(ctx context.Context) (string, error) {
	form := url.Values{
		"username": {m.Username},
		"password": {m.Password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.JWTURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return doMint(req, m.Cookie, m.HTTP)
}

// CookieMinter mints via GET jwt.php authenticated by the NG session or
// remember cookie — the long-term path for regular users (ADR 0001). It
// already works today for any logged-in NG account.
type CookieMinter struct {
	JWTURL string
	// NGCookie is the newgrounds.com cookie header value granting the
	// session, e.g. "vmkldu5I8m=...".
	NGCookie string
	// Cookie is an extra routing cookie for the dev proxy, appended to
	// NGCookie.
	Cookie string
	HTTP   *http.Client
}

// Mint implements Minter.
func (m *CookieMinter) Mint(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.JWTURL, nil)
	if err != nil {
		return "", err
	}
	cookie := m.NGCookie
	if m.Cookie != "" {
		cookie += "; " + m.Cookie
	}
	return doMint(req, cookie, m.HTTP)
}

// doMint executes a prepared jwt.php request and extracts the token,
// normalizing the endpoint's mixed JSend/Laravel error shapes.
func doMint(req *http.Request, cookie string, hc *http.Client) (string, error) {
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("Accept", "application/json")
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("jwt request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("jwt response: %w", err)
	}

	var ok jsend
	if json.Unmarshal(body, &ok) == nil && ok.Status == "success" &&
		ok.Data.Token != "" {
		return ok.Data.Token, nil
	}

	msg := ok.Message
	if msg == "" {
		var le laravelError
		if json.Unmarshal(body, &le) == nil {
			msg = le.Message
		}
	}
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return "", fmt.Errorf("jwt mint failed (HTTP %d): %s",
		resp.StatusCode, msg)
}
