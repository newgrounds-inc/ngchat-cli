package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/newgrounds-inc/ngchat-cli/internal/render"
)

// maxCodeAttempts is how many wrong two-factor codes are accepted before
// login restarts from the password step. The site's own limiter is the
// real ceiling; this keeps a mistyped authenticator from burning it.
const maxCodeAttempts = 3

// Prompter collects login input. The terminal implementation lives in
// cmd/ngchat so this flow can be driven by a script in tests.
type Prompter interface {
	// Line reads a visible line, trimmed.
	Line(prompt string) (string, error)
	// Secret reads a line without echo.
	Secret(prompt string) (string, error)
}

// Session is what a completed login yields: who logged in, and the
// remember cookie value to persist.
type Session struct {
	User     User
	Remember string
}

// IdentityHelp is shown before the identity prompt. The site answers an
// email-only account's username with the same 422 as a wrong password,
// on purpose, so the CLI cannot detect that case and says so up front.
const IdentityHelp = "Use your email address if your account is set to " +
	"log in with email only."

// PasswordHelp is shown before the password prompt. An empty password is
// never sent: the site would only answer 422 and count it against the
// login limiter, so the entry doubles as the way back to the identity.
const PasswordHelp = "Press Enter with no password to change the username."

// Login walks the password and two-factor steps until the jar holds a
// remember cookie. Wrong credentials re-prompt from the password step;
// three wrong codes or an expired challenge restart there too; an empty
// password goes back to the identity prompt. Lockout (429) and an
// undeliverable code (422) end the flow with the site's message, since
// only waiting or support can fix them.
func Login(ctx context.Context, site *Site, p Prompter, out io.Writer) (Session, error) {
	fmt.Fprintln(out, IdentityHelp)
	identity := ""
	for {
		var err error
		identity, err = askIdentity(p, identity)
		if err != nil {
			return Session{}, err
		}
		fmt.Fprintln(out, PasswordHelp)
		sess, back, err := passwordStep(ctx, site, p, out, identity)
		if err != nil {
			return Session{}, err
		}
		if !back {
			return sess, nil
		}
	}
}

// askIdentity prompts for the identity, offering the previous one as the
// default so a stray Enter at the password prompt costs nothing.
func askIdentity(p Prompter, prev string) (string, error) {
	for {
		prompt := "Newgrounds username or email: "
		if prev != "" {
			prompt = fmt.Sprintf("Newgrounds username or email [%s]: ", prev)
		}
		identity, err := p.Line(prompt)
		if err != nil {
			return "", err
		}
		identity = strings.TrimSpace(identity)
		switch {
		case identity != "":
			return identity, nil
		case prev != "":
			return prev, nil
		}
	}
}

// passwordStep loops on the password prompt for one identity. back is
// true when the user asked to change the identity.
func passwordStep(ctx context.Context, site *Site, p Prompter, out io.Writer,
	identity string) (Session, bool, error) {
	for {
		password, err := p.Secret("Password: ")
		if err != nil {
			return Session{}, false, err
		}
		if password == "" {
			return Session{}, true, nil
		}
		res, err := site.Login(ctx, identity, password)
		if err != nil {
			if retryable(err) {
				fmt.Fprintln(out, err.Error())
				continue
			}
			return Session{}, false, err
		}
		if res.TwoFactor == "" {
			sess, err := session(site, res.User)
			return sess, false, err
		}
		user, done, err := challenge(ctx, site, p, out, res)
		if err != nil {
			return Session{}, false, err
		}
		if done {
			sess, err := session(site, user)
			return sess, false, err
		}
	}
}

// challenge runs the code prompts for one login attempt. done is false
// when the flow should restart from the password step.
func challenge(ctx context.Context, site *Site, p Prompter, out io.Writer,
	res LoginResult) (User, bool, error) {
	switch res.TwoFactor {
	case "email":
		fmt.Fprintf(out, "A code was emailed to %s; it is valid for one hour.\n",
			render.Line(res.ObfuscatedEmail))
	case "totp":
		fmt.Fprintln(out, "Enter the code from your authenticator app, "+
			"or a recovery code.")
	default:
		return User{}, false, fmt.Errorf(
			"login: unknown two-factor method %q", res.TwoFactor)
	}
	for attempts := 0; attempts < maxCodeAttempts; {
		code, err := p.Line("Code: ")
		if err != nil {
			return User{}, false, err
		}
		if strings.TrimSpace(code) == "" {
			continue
		}
		user, err := site.TwoFactor(ctx, code)
		if err == nil {
			return user, true, nil
		}
		if errors.Is(err, ErrNoChallenge) {
			fmt.Fprintln(out, "The login challenge expired; starting over.")
			return User{}, false, nil
		}
		if !retryable(err) {
			return User{}, false, err
		}
		attempts++
		fmt.Fprintln(out, err.Error())
	}
	fmt.Fprintln(out, "Too many wrong codes; starting over from the password.")
	return User{}, false, nil
}

// retryable is true for a 422 the user can fix by typing again. Lockout
// and an undeliverable code are 4xx too but end the flow.
func retryable(err error) bool {
	var fail *FailError
	if !errors.As(err, &fail) {
		return false
	}
	return fail.Status == http.StatusUnprocessableEntity &&
		!fail.Has("undeliverable")
}

// session reads the remember cookie the login set. Its absence means the
// site did not honor remember=true, which nothing here can recover from.
func session(site *Site, user User) (Session, error) {
	remember, ok := site.Remember()
	if !ok {
		return Session{}, errors.New(
			"login succeeded but the site set no remember cookie")
	}
	return Session{User: user, Remember: remember}, nil
}
