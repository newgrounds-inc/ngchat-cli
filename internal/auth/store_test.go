package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
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
				return "", keyring.ErrNotFound
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
	memKeyring(t)

	// Deleting a key that was never stored is a no-op, not an error — this
	// is what `ngchat logout` does on a fresh machine.
	if err := (Store{}).Delete("ngchat-test-absent-key"); err != nil {
		t.Errorf("Delete on missing key: %v", err)
	}
}

// TestDeleteReportsUnavailableKeyring: with no keyring answering, the
// file copy still goes, but the result says the keyring went unchecked
// rather than claiming both are clear.
func TestDeleteReportsUnavailableKeyring(t *testing.T) {
	tempConfigDir(t)
	noKeyring(t)
	if err := fileSave(RememberKey, "v"); err != nil {
		t.Fatal(err)
	}
	err := (Store{}).Delete(RememberKey)
	if !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Delete = %v, want ErrKeyringUnavailable", err)
	}
	if _, err := fileLoad(RememberKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("file copy survived: %v", err)
	}
}

// TestDeleteReportsKeyringSurvivor is the locked-or-denied case: the
// keyring refuses the delete but still serves the value, so Load would
// keep finding it and Delete must say so.
func TestDeleteReportsKeyringSurvivor(t *testing.T) {
	tempConfigDir(t)
	m := memKeyring(t)
	m[RememberKey] = "still-here"
	kr.delete = func(_, _ string) error { return errors.New("keyring locked") }

	err := (Store{}).Delete(RememberKey)
	if err == nil || errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Delete = %v, want a hard error naming the survivor", err)
	}
}

// TestDeleteReportsCorruptFile: a credentials file that cannot be parsed
// may hold the key, so a delete that cannot prove otherwise is not a
// success.
func TestDeleteReportsCorruptFile(t *testing.T) {
	tempConfigDir(t)
	memKeyring(t)
	path, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"ng_remember": "v"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Store{}).Delete(RememberKey); err == nil {
		t.Error("Delete reported success over an unparseable file")
	}
}

// TestSaveRefusesStaleKeyringSurvivor: a keyring that cannot take the
// new value but keeps serving the old one would win on Load, so the
// Save fails instead of writing a file nobody reads.
func TestSaveRefusesStaleKeyringSurvivor(t *testing.T) {
	tempConfigDir(t)
	m := memKeyring(t)
	m[RememberKey] = "old-account"
	kr.set = func(_, _, _ string) error { return errors.New("locked") }
	kr.delete = func(_, _ string) error { return errors.New("locked") }

	if err := (Store{}).Save(RememberKey, "new-account"); err == nil {
		t.Fatal("Save succeeded while the keyring still serves the old value")
	}
	if _, err := fileLoad(RememberKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("file written despite the failure: %v", err)
	}
}

// TestMigrateToleratesUnavailableKeyring: the legacy slot was found in
// the file, so a keyring that does not answer must not turn every run
// into a fatal error.
func TestMigrateToleratesUnavailableKeyring(t *testing.T) {
	tempConfigDir(t)
	noKeyring(t)
	if err := fileSave(legacyCookieKey, "header"); err != nil {
		t.Fatal(err)
	}
	removed, err := (Store{}).Migrate()
	if err != nil || !removed {
		t.Errorf("Migrate = %v, %v; want true, nil", removed, err)
	}
}

// TestFileWriteRepairsMode: os.WriteFile keeps an existing file's mode,
// so a restored or chmodded copy would stay readable; every write must
// leave 0600 behind, and a symlink at the path must be replaced, not
// followed.
func TestFileWriteRepairsMode(t *testing.T) {
	tempConfigDir(t)
	path, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fileSave(RememberKey, "secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode after rewrite = %o, want 600", perm)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode after rewrite = %o, want 700", perm)
	}
	if got, _ := fileLoad(RememberKey); got != "secret" {
		t.Errorf("Load = %q", got)
	}
}

func TestFileWriteReplacesSymlink(t *testing.T) {
	tempConfigDir(t)
	path, err := credentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := fileSave(RememberKey, "secret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("credentials path is still a %v, want a regular file", info.Mode().Type())
	}
	if raw, _ := os.ReadFile(target); string(raw) != "{}" {
		t.Errorf("symlink target was written through: %q", raw)
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
			// With no keyring the answer is "unreadable", not "absent";
			// either way the slot must not load a value.
			if v, err := s.Load(legacyCookieKey); err == nil ||
				(!errors.Is(err, ErrNotFound) && !errors.Is(err, ErrKeyringUnavailable)) {
				t.Errorf("legacy slot still loads: %q, %v", v, err)
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

// TestSaveWinsOverKeyringThatUnlocksLater is the lock-then-unlock case:
// the keyring holds the old account but answers nothing while locked,
// the new account lands in the file, and when the keyring opens again
// Load must still return the new one.
func TestSaveWinsOverKeyringThatUnlocksLater(t *testing.T) {
	tempConfigDir(t)
	m := memKeyring(t)
	m[RememberKey] = "old-account"
	unlocked := kr
	locked := errors.New("keyring locked")
	kr = keyringBackend{
		get:    func(_, _ string) (string, error) { return "", locked },
		set:    func(_, _, _ string) error { return locked },
		delete: func(_, _ string) error { return locked },
	}

	var s Store
	if err := s.Save(RememberKey, "new-account"); err != nil {
		t.Fatalf("Save with a locked keyring: %v", err)
	}
	if got, _ := s.Load(RememberKey); got != "new-account" {
		t.Errorf("Load while locked = %q", got)
	}

	kr = unlocked
	if got, _ := s.Load(RememberKey); got != "new-account" {
		t.Errorf("Load after unlock = %q, want the account saved while locked", got)
	}
	if m[RememberKey] != "old-account" {
		t.Fatal("test premise broken: the locked keyring lost its entry")
	}
	// The next successful keyring Save clears the file, and only then
	// is the keyring the one that answers.
	if err := s.Save(RememberKey, "third"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load(RememberKey); got != "third" {
		t.Errorf("Load after a keyring Save = %q", got)
	}
	if _, err := fileLoad(RememberKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("file copy survived a keyring-backed Save: %v", err)
	}
}

// TestLoadDoesNotInferAbsenceFromUnreadableKeyring: with no file value
// and a keyring that gives no answer, "not found" would send a logout
// down the nothing-to-revoke path while a live credential may sit
// behind the failure. The file still wins when it has the key.
func TestLoadDoesNotInferAbsenceFromUnreadableKeyring(t *testing.T) {
	tempConfigDir(t)
	noKeyring(t)
	var s Store
	if _, err := s.Load(RememberKey); !errors.Is(err, ErrKeyringUnavailable) {
		t.Errorf("Load = %v, want ErrKeyringUnavailable", err)
	}
	if err := fileSave(RememberKey, "v"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(RememberKey); err != nil || got != "v" {
		t.Errorf("Load with a file value = %q, %v", got, err)
	}
	removed, err := s.Migrate()
	if err != nil || removed {
		t.Errorf("Migrate with an unreadable keyring = %v, %v; want false, nil",
			removed, err)
	}
}
