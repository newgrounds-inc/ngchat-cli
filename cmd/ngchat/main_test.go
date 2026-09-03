package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/newgrounds-inc/ngchat-cli/internal/auth"
)

// TestTermPrompterCancels: a prompt blocked on stdin must return as soon
// as the context ends, which is what makes Ctrl-C work at the login
// prompts instead of restarting the read.
func TestTermPrompterCancels(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	p := termPrompter{ctx: ctx, in: bufio.NewReader(pr), fd: -1}

	done := make(chan error, 1)
	go func() {
		_, err := p.Line("identity: ")
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Line did not return after the context was canceled")
	}
}

func TestTermPrompterReadsLine(t *testing.T) {
	pr, pw := io.Pipe()
	go func() { io.WriteString(pw, "  bob \n"); pw.Close() }()
	p := termPrompter{ctx: context.Background(), in: bufio.NewReader(pr), fd: -1}
	got, err := p.Line("identity: ")
	if err != nil || got != "bob" {
		t.Errorf("Line = %q, %v; want \"bob\"", got, err)
	}
	// Secret on a non-TTY falls back to a plain line, so piped input works.
	pr2, pw2 := io.Pipe()
	go func() { io.WriteString(pw2, "hunter2\n"); pw2.Close() }()
	p.in = bufio.NewReader(pr2)
	got, err = p.Secret("password: ")
	if err != nil || got != "hunter2" {
		t.Errorf("Secret = %q, %v", got, err)
	}
}

// TestDebugLogPathHonorsXDG pins the log under the state directory and
// checks the file opens with a private mode.
func TestDebugLogPathHonorsXDG(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path, err := debugLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(state, "ngchat", "debug.log"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	f, err := openDebugLog(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX mode bits: Go reports 0666 for any writable
	// file there, so the check only means something on Unix.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
	if !strings.HasPrefix(path, state) {
		t.Errorf("path %q escaped the state directory", path)
	}
}

// TestDebugLogRepairsMode: a log left with a wide mode, or a symlink
// planted at its path, must not be written through on the next run.
func TestDebugLogRepairsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX mode bits or symlinks to check on Windows")
	}
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path, err := debugLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openDebugLog(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after rewrite = %o, want 0600", info.Mode().Perm())
	}
	if info.Size() != 0 {
		t.Errorf("old contents survived: size %d", info.Size())
	}

	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if f, err := openDebugLog(path); err == nil {
		f.Close()
		t.Error("openDebugLog wrote through a symlink")
	}
	if raw, _ := os.ReadFile(target); string(raw) != "keep" {
		t.Errorf("symlink target changed: %q", raw)
	}
}

// fakeStore and fakeSite script the logout outcome matrix without a
// keyring or a network.
type fakeStore struct {
	loadValue string
	loadErr   error
	deleteErr error
	deletes   int
}

func (f *fakeStore) Load(string) (string, error) { return f.loadValue, f.loadErr }
func (f *fakeStore) Delete(string) error         { f.deletes++; return f.deleteErr }
func (f *fakeStore) Migrate() (bool, error)      { return false, nil }

type fakeSite struct {
	logoutErr error
	logouts   int
	seeded    string
}

func (f *fakeSite) CredentialKey() string        { return auth.RememberKey }
func (f *fakeSite) SetRemember(v string)         { f.seeded = v }
func (f *fakeSite) Logout(context.Context) error { f.logouts++; return f.logoutErr }

func TestLogoutOutcomes(t *testing.T) {
	unavailable := fmt.Errorf("%w: no secret service", auth.ErrKeyringUnavailable)
	tests := []struct {
		name        string
		store       fakeStore
		site        fakeSite
		wantCode    int
		wantLogouts int
		wantOut     string
		wantErr     string
	}{
		{"online, stored",
			fakeStore{loadValue: "cookie"}, fakeSite{},
			0, 1, "logged out\n", ""},
		{"offline: local cleanup still runs, exit 1",
			fakeStore{loadValue: "cookie"}, fakeSite{logoutErr: errors.New("dial tcp: refused")},
			1, 1, "", "did not revoke"},
		{"nothing stored, keyring answers",
			fakeStore{loadErr: auth.ErrNotFound}, fakeSite{},
			0, 0, "no stored credentials\n", ""},
		{"nothing stored, keyring unavailable: cannot prove it is empty",
			fakeStore{loadErr: auth.ErrNotFound, deleteErr: unavailable}, fakeSite{},
			1, 0, "", "not revoked"},
		{"stored in file, keyring unavailable, revoked: warning only",
			fakeStore{loadValue: "cookie", deleteErr: unavailable}, fakeSite{},
			0, 1, "logged out\n", "OS keyring unavailable"},
		{"stored in file, keyring unavailable, offline: exit 1",
			fakeStore{loadValue: "cookie", deleteErr: unavailable},
			fakeSite{logoutErr: errors.New("refused")},
			1, 1, "", "not revoked"},
		{"locked keyring still holds the value",
			fakeStore{loadValue: "cookie", deleteErr: errors.New("keyring still holds ng_remember: locked")},
			fakeSite{},
			1, 1, "", "not fully cleared"},
		{"keyring unreadable on load: absence is not assumed",
			fakeStore{loadErr: unavailable, deleteErr: unavailable},
			fakeSite{},
			1, 0, "", "nothing was revoked"},
		{"corrupt store: nothing revoked, cleanup attempted",
			fakeStore{loadErr: errors.New("parsing credentials.json: unexpected end")},
			fakeSite{},
			1, 0, "", "nothing was revoked"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			store, site := tc.store, tc.site
			code := logout(context.Background(), &store, &site, &out, &errOut)
			if code != tc.wantCode {
				t.Errorf("exit = %d, want %d (stderr %q)", code, tc.wantCode, errOut.String())
			}
			if site.logouts != tc.wantLogouts {
				t.Errorf("site logout calls = %d, want %d", site.logouts, tc.wantLogouts)
			}
			if store.deletes != 1 {
				t.Errorf("local delete calls = %d, want 1", store.deletes)
			}
			if out.String() != tc.wantOut {
				t.Errorf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
			if tc.wantErr != "" && !strings.Contains(errOut.String(), tc.wantErr) {
				t.Errorf("stderr = %q, want it to mention %q", errOut.String(), tc.wantErr)
			}
			if tc.wantErr == "" && errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
			if tc.wantLogouts == 1 && site.seeded != tc.store.loadValue {
				t.Errorf("seeded %q into the jar, want %q", site.seeded, tc.store.loadValue)
			}
		})
	}
}
