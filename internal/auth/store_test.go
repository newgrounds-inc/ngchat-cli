package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// tempConfigDir points os.UserConfigDir at a scratch directory so tests never
// touch the real credential file. The OS keyring is deliberately left alone:
// these tests exercise the fallback path only, since writing to a developer's
// actual keyring would be a side effect, not a test.
func tempConfigDir(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("config dir override is not portable to Windows")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

func TestFileStoreRoundTrip(t *testing.T) {
	tempConfigDir(t)

	if err := fileSave("ng_cookie", "vmkldu5I8m=abc"); err != nil {
		t.Fatalf("fileSave: %v", err)
	}
	got, err := fileLoad("ng_cookie")
	if err != nil {
		t.Fatalf("fileLoad: %v", err)
	}
	if got != "vmkldu5I8m=abc" {
		t.Errorf("loaded %q", got)
	}

	// A second key must not clobber the first.
	if err := fileSave("other", "value"); err != nil {
		t.Fatalf("fileSave: %v", err)
	}
	if got, _ := fileLoad("ng_cookie"); got != "vmkldu5I8m=abc" {
		t.Errorf("first key lost after second save: %q", got)
	}
}

func TestFileLoadMissingKey(t *testing.T) {
	tempConfigDir(t)

	if _, err := fileLoad("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if err := fileSave("present", "v"); err != nil {
		t.Fatalf("fileSave: %v", err)
	}
	if _, err := fileLoad("absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestCredentialFilePermissions is the security-relevant assertion: the
// fallback file holds the NG remember cookie in plaintext, so it must not be
// group- or world-readable.
func TestCredentialFilePermissions(t *testing.T) {
	tempConfigDir(t)

	if err := fileSave("ng_cookie", "secret"); err != nil {
		t.Fatalf("fileSave: %v", err)
	}
	path, err := credentialsPath()
	if err != nil {
		t.Fatalf("credentialsPath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode = %o, want 600", perm)
	}

	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 700", perm)
	}
}

// TestFileReadRejectsCorruptFile surfaces a hand-mangled credentials file as
// an error rather than silently starting from empty and losing the cookie.
func TestFileReadRejectsCorruptFile(t *testing.T) {
	tempConfigDir(t)

	path, err := credentialsPath()
	if err != nil {
		t.Fatalf("credentialsPath: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := fileLoad("ng_cookie"); err == nil {
		t.Error("expected a parse error for a corrupt credentials file")
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	tempConfigDir(t)

	// Deleting a key that was never stored is a no-op, not an error — this
	// is what `ngchat logout` does on a fresh machine.
	if err := (Store{}).Delete("ngchat-test-absent-key"); err != nil {
		t.Errorf("Delete on missing key: %v", err)
	}
}
