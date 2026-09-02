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

// noKeyring swaps the keyring backend for one that always fails, which
// is what a headless box looks like, so Store methods exercise the file
// fallback without touching the developer's real keyring.
func noKeyring(t *testing.T) {
	t.Helper()
	saved := kr
	t.Cleanup(func() { kr = saved })
	unavailable := errors.New("no secret service")
	kr = keyringBackend{
		get:    func(_, _ string) (string, error) { return "", unavailable },
		set:    func(_, _, _ string) error { return unavailable },
		delete: func(_, _ string) error { return unavailable },
	}
}

// memKeyring swaps in an in-memory keyring so the preferred path is
// covered too.
func memKeyring(t *testing.T) map[string]string {
	t.Helper()
	saved := kr
	t.Cleanup(func() { kr = saved })
	m := map[string]string{}
	kr = keyringBackend{
		get: func(_, k string) (string, error) {
			v, ok := m[k]
			if !ok {
				return "", errors.New("not found")
			}
			return v, nil
		},
		set:    func(_, k, v string) error { m[k] = v; return nil },
		delete: func(_, k string) error { delete(m, k); return nil },
	}
	return m
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
	noKeyring(t)

	// Deleting a key that was never stored is a no-op, not an error — this
	// is what `ngchat logout` does on a fresh machine.
	if err := (Store{}).Delete("ngchat-test-absent-key"); err != nil {
		t.Errorf("Delete on missing key: %v", err)
	}
}

func TestStorePrefersKeyring(t *testing.T) {
	tempConfigDir(t)
	m := memKeyring(t)
	var s Store
	if err := s.Save(RememberKey, "v1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if m[RememberKey] != "v1" {
		t.Error("value did not land in the keyring")
	}
	if _, err := fileLoad(RememberKey); !errors.Is(err, ErrNotFound) {
		t.Error("value leaked into the fallback file while a keyring exists")
	}
	if got, _ := s.Load(RememberKey); got != "v1" {
		t.Errorf("Load = %q", got)
	}
	if err := s.Delete(RememberKey); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load(RememberKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load after Delete = %v, want ErrNotFound", err)
	}
}

func TestStoreFallsBackToFile(t *testing.T) {
	tempConfigDir(t)
	noKeyring(t)
	var s Store
	if err := s.Save(RememberKey, "v1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, err := s.Load(RememberKey); err != nil || got != "v1" {
		t.Errorf("Load = %q, %v", got, err)
	}
}

// TestMigrateRemovesLegacyCookieHeader: the v0.1 slot held a browser
// cookie header that cannot become a remember value, so migration
// deletes it and reports that a login is needed.
func TestMigrateRemovesLegacyCookieHeader(t *testing.T) {
	for _, backend := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"file", noKeyring},
		{"keyring", func(t *testing.T) { memKeyring(t) }},
	} {
		t.Run(backend.name, func(t *testing.T) {
			tempConfigDir(t)
			backend.setup(t)
			var s Store
			if err := s.Save(legacyCookieKey, "vmkldu5I8m=abc"); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if err := s.Save(RememberKey, "keep"); err != nil {
				t.Fatalf("Save: %v", err)
			}
			removed, err := s.Migrate()
			if err != nil || !removed {
				t.Fatalf("Migrate = %v, %v; want true, nil", removed, err)
			}
			if _, err := s.Load(legacyCookieKey); !errors.Is(err, ErrNotFound) {
				t.Errorf("legacy slot still loads: %v", err)
			}
			if got, _ := s.Load(RememberKey); got != "keep" {
				t.Errorf("remember slot disturbed: %q", got)
			}
			removed, err = s.Migrate()
			if err != nil || removed {
				t.Errorf("second Migrate = %v, %v; want false, nil", removed, err)
			}
		})
	}
}

// TestSaveClearsTheOtherBackend is the two-accounts bug: Load prefers
// the keyring, so a Save that lands in the file while an older keyring
// entry survives (write failed, read still works) must remove that
// entry, and a Save that lands in the keyring must drop a stale file
// copy.
func TestSaveClearsTheOtherBackend(t *testing.T) {
	t.Run("keyring write fails, old entry lingers", func(t *testing.T) {
		tempConfigDir(t)
		m := memKeyring(t)
		m[RememberKey] = "old-account"
		saved := kr
		t.Cleanup(func() { kr = saved })
		kr.set = func(_, _, _ string) error { return errors.New("locked") }

		var s Store
		if err := s.Save(RememberKey, "new-account"); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if got, _ := s.Load(RememberKey); got != "new-account" {
			t.Errorf("Load = %q, want the account just saved", got)
		}
		if _, ok := m[RememberKey]; ok {
			t.Error("stale keyring entry survived a file-backed Save")
		}
	})
	t.Run("keyring write succeeds, stale file copy", func(t *testing.T) {
		tempConfigDir(t)
		if err := fileSave(RememberKey, "old-account"); err != nil {
			t.Fatalf("fileSave: %v", err)
		}
		memKeyring(t)
		var s Store
		if err := s.Save(RememberKey, "new-account"); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if _, err := fileLoad(RememberKey); !errors.Is(err, ErrNotFound) {
			t.Errorf("stale file copy survived a keyring-backed Save: %v", err)
		}
	})
}
