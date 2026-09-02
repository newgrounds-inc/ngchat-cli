package auth

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// scriptPrompter answers prompts from fixed lists and fails once they run
// out, so a flow that loops forever ends the test instead of hanging.
type scriptPrompter struct {
	lines   []string
	secrets []string
	asked   []string
}

var errScriptExhausted = errors.New("prompter script exhausted")

func (p *scriptPrompter) Line(prompt string) (string, error) {
	p.asked = append(p.asked, prompt)
	if len(p.lines) == 0 {
		return "", errScriptExhausted
	}
	v := p.lines[0]
	p.lines = p.lines[1:]
	return v, nil
}

func (p *scriptPrompter) Secret(prompt string) (string, error) {
	p.asked = append(p.asked, prompt)
	if len(p.secrets) == 0 {
		return "", errScriptExhausted
	}
	v := p.secrets[0]
	p.secrets = p.secrets[1:]
	return v, nil
}

func (p *scriptPrompter) count(prompt string) int {
	n := 0
	for _, a := range p.asked {
		if a == prompt {
			n++
		}
	}
	return n
}

func twoFactorLogin(method string) func(map[string]any) (int, string) {
	return func(map[string]any) (int, string) {
		return 200, `{"status":"success","data":{"two_factor":"` + method +
			`","obfuscated_email":"b***@example.com"}}`
	}
}

func TestLoginFlowWithoutTwoFactor(t *testing.T) {
	fs := newFakeSite(t)
	p := &scriptPrompter{lines: []string{"bob"}, secrets: []string{"pw"}}
	var out bytes.Buffer
	sess, err := Login(context.Background(), fs.site(t), p, &out)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.User.Username != "bob" || sess.Remember != fakeRemember {
		t.Errorf("session = %+v", sess)
	}
	if !strings.Contains(out.String(), IdentityHelp) {
		t.Error("identity help text not shown")
	}
}

func TestLoginFlowEmailCode(t *testing.T) {
	fs := newFakeSite(t)
	fs.login = twoFactorLogin("email")
	p := &scriptPrompter{lines: []string{"bob", "123456"}, secrets: []string{"pw"}}
	var out bytes.Buffer
	sess, err := Login(context.Background(), fs.site(t), p, &out)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.Remember != fakeRemember {
		t.Errorf("remember = %q", sess.Remember)
	}
	if !strings.Contains(out.String(), "b***@example.com") {
		t.Errorf("email hint missing from output: %q", out.String())
	}
	if body := fs.lastBody(twoFactorPath); body["code"] != "123456" {
		t.Errorf("two-factor body = %v", body)
	}
}

func TestLoginFlowTOTPRecoveryCode(t *testing.T) {
	fs := newFakeSite(t)
	fs.login = twoFactorLogin("totp")
	p := &scriptPrompter{lines: []string{"bob", "abcd-efgh"}, secrets: []string{"pw"}}
	var out bytes.Buffer
	if _, err := Login(context.Background(), fs.site(t), p, &out); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if body := fs.lastBody(twoFactorPath); body["recovery_code"] != "abcd-efgh" {
		t.Errorf("two-factor body = %v, want recovery_code", body)
	}
	if !strings.Contains(out.String(), "recovery code") {
		t.Error("totp hint missing")
	}
}

// TestLoginFlowBadPasswordReprompts: a 422 re-asks the password only; the
// identity is kept.
func TestLoginFlowBadPasswordReprompts(t *testing.T) {
	fs := newFakeSite(t)
	calls := 0
	fs.login = func(body map[string]any) (int, string) {
		calls++
		if calls == 1 {
			return 422, jsendFail("identity", "These credentials do not match our records.")
		}
		return 200, `{"status":"success","data":{"two_factor":null,"user":{"id":7,"username":"bob"}}}`
	}
	p := &scriptPrompter{lines: []string{"bob"}, secrets: []string{"wrong", "right"}}
	var out bytes.Buffer
	if _, err := Login(context.Background(), fs.site(t), p, &out); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.Contains(out.String(), "do not match") {
		t.Error("site message not shown")
	}
	if p.count("Password: ") != 2 || p.count("Newgrounds username or email: ") != 1 {
		t.Errorf("prompts = %v", p.asked)
	}
	if fs.lastBody(loginPath)["password"] != "right" {
		t.Error("second attempt did not send the new password")
	}
}

// TestLoginFlowExits covers the outcomes that end the flow with the
// site's message instead of re-prompting.
func TestLoginFlowExits(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"lockout", 429,
			jsendFail("identity", "Too many login attempts. Please try again in 57 seconds."),
			"Too many login attempts. Please try again in 57 seconds."},
		{"undeliverable", 422,
			jsendFail("undeliverable", "We cannot send a code to your email address."),
			"We cannot send a code to your email address."},
		{"server error", 500, `{"status":"error","message":"boom"}`, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeSite(t)
			fs.login = func(map[string]any) (int, string) { return tc.status, tc.body }
			p := &scriptPrompter{lines: []string{"bob"}, secrets: []string{"pw", "again"}}
			_, err := Login(context.Background(), fs.site(t), p, &bytes.Buffer{})
			if err == nil || err.Error() != tc.want {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if p.count("Password: ") != 1 {
				t.Errorf("password prompted %d times, want 1", p.count("Password: "))
			}
		})
	}
}

// TestLoginFlowThreeWrongCodesRestart: after three 422s on the code the
// flow goes back to the password step and a fresh login.
func TestLoginFlowThreeWrongCodesRestart(t *testing.T) {
	fs := newFakeSite(t)
	fs.login = twoFactorLogin("email")
	fs.twoFactor = func(body map[string]any) (int, string) {
		if body["code"] == "999999" {
			return 200, `{"status":"success","data":{"user":{"id":7,"username":"bob"}}}`
		}
		return 422, jsendFail("code", "The code is invalid.")
	}
	p := &scriptPrompter{
		lines:   []string{"bob", "111111", "", "222222", "333333", "999999"},
		secrets: []string{"pw", "pw"},
	}
	var out bytes.Buffer
	sess, err := Login(context.Background(), fs.site(t), p, &out)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.Remember != fakeRemember {
		t.Errorf("remember = %q", sess.Remember)
	}
	if fs.count(loginPath) != 2 {
		t.Errorf("login calls = %d, want 2 (restart after three wrong codes)",
			fs.count(loginPath))
	}
	if fs.count(twoFactorPath) != 4 {
		t.Errorf("two-factor calls = %d, want 4 (blank input is not an attempt)",
			fs.count(twoFactorPath))
	}
	if p.count("Password: ") != 2 {
		t.Errorf("password prompted %d times, want 2", p.count("Password: "))
	}
	if strings.Count(out.String(), "The code is invalid.") != 3 {
		t.Errorf("site message shown %d times, want 3",
			strings.Count(out.String(), "The code is invalid."))
	}
}

// TestLoginFlowStaleChallengeRestarts: a 403 on two-factor means the
// challenge is gone, so the flow restarts from the password step at once.
func TestLoginFlowStaleChallengeRestarts(t *testing.T) {
	fs := newFakeSite(t)
	logins := 0
	fs.login = func(map[string]any) (int, string) {
		logins++
		if logins == 1 {
			return 200, `{"status":"success","data":{"two_factor":"email","obfuscated_email":"b***@x"}}`
		}
		return 200, `{"status":"success","data":{"two_factor":null,"user":{"id":7,"username":"bob"}}}`
	}
	fs.twoFactor = func(map[string]any) (int, string) {
		// Consume the challenge as an expired one would be, then refuse.
		fs.challenge = ""
		return 403, jsendFail("http", "This action is unauthorized.")
	}
	p := &scriptPrompter{lines: []string{"bob", "123456"}, secrets: []string{"pw", "pw"}}
	var out bytes.Buffer
	if _, err := Login(context.Background(), fs.site(t), p, &out); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.Contains(out.String(), "expired") {
		t.Errorf("no restart notice in output: %q", out.String())
	}
	if p.count("Code: ") != 1 || p.count("Password: ") != 2 {
		t.Errorf("prompts = %v, want one code then a second password", p.asked)
	}
}

func TestLoginFlowTwoFactorLockoutExits(t *testing.T) {
	fs := newFakeSite(t)
	fs.login = twoFactorLogin("totp")
	fs.twoFactor = func(map[string]any) (int, string) {
		return 429, jsendFail("identity", "Too many login attempts. Please try again in 30 seconds.")
	}
	p := &scriptPrompter{lines: []string{"bob", "123456", "123456"}, secrets: []string{"pw"}}
	_, err := Login(context.Background(), fs.site(t), p, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "30 seconds") {
		t.Errorf("err = %v, want the lockout message", err)
	}
	if p.count("Code: ") != 1 {
		t.Errorf("code prompted %d times, want 1", p.count("Code: "))
	}
}

func TestLoginFlowPrompterErrorAborts(t *testing.T) {
	fs := newFakeSite(t)
	p := &scriptPrompter{}
	_, err := Login(context.Background(), fs.site(t), p, &bytes.Buffer{})
	if !errors.Is(err, errScriptExhausted) {
		t.Errorf("err = %v, want the prompter's error", err)
	}
	if fs.count(loginPath) != 0 {
		t.Error("login attempted without an identity")
	}
}
