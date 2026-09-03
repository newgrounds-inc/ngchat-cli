// Package auth handles credential storage, site login, and chat-JWT
// minting.
//
// The stored credential is the value of the site's long-lived remember
// cookie — never the account password (ADR 0001, ADR 0003).
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const keyringService = "ngchat"

// RememberKey is the credential-store slot for the production site's
// remember cookie value. Other sites get their own slot (Site.CredentialKey)
// so a credential is only ever presented to the origin that issued it.
const RememberKey = "ng_remember"

// legacyCookieKey held a whole Cookie header in v0.1 (pasted from a
// browser). Its session cookie is long dead and the header cannot be
// turned into a remember value, so migration deletes it and the user
// logs in once.
const legacyCookieKey = "ng_cookie"

// ErrNotFound is returned by Load when no value is stored under a key.
var ErrNotFound = errors.New("credential not found")

// ErrKeyringUnavailable marks an OS keyring that gave no answer: there
// is none (a headless box), or it is locked or denied. Load returns it
// when the file has no value and the keyring could not be read, so that
// absence is never inferred from a backend that did not answer; Delete
// returns it when the key could neither be removed nor read back. Save
// carries on with the file, since Load reads the file first.
var ErrKeyringUnavailable = errors.New("OS keyring unavailable")

// keyringBackend is the OS keyring surface, swappable so tests never
// touch a developer's real keyring.
type keyringBackend struct {
	get    func(service, user string) (string, error)
	set    func(service, user, password string) error
	delete func(service, user string) error
}

var kr = keyringBackend{
	get:    keyring.Get,
	set:    keyring.Set,
	delete: keyring.Delete,
}

// Store persists secrets in the OS keyring (macOS Keychain, Windows
// Credential Manager, Linux Secret Service), falling back to a 0600 JSON
// file under the user config dir on headless boxes without a keyring.
type Store struct{}

// Save stores a secret under key, preferring the keyring. Whichever
// backend takes the value, the other is cleared, so at most one holds
// it. When the keyring refuses the write, the stale entry it may hold
// is removed on a best-effort basis: a keyring that still serves it and
// refuses to drop it fails the Save (something is wrong with it), but
// one that answers nothing, absent or locked, is tolerated, because
// Load reads the file first and the file is what this Save writes, so
// an entry that resurfaces when the keyring unlocks cannot shadow it.
func (Store) Save(key, value string) error {
	if err := kr.set(keyringService, key, value); err == nil {
		return fileDelete(key)
	}
	if err := keyringClear(key); err != nil && !errors.Is(err, ErrKeyringUnavailable) {
		return err
	}
	return fileSave(key, value)
}

// Load retrieves a secret, checking the fallback file then the keyring.
// The file comes first because a Save only lands there after the
// keyring refused the value, and a keyring that was locked at the time
// may still hold the previous account's entry once it opens; the file
// is the more recent write whenever it has the key. Returns ErrNotFound
// only when both backends answered that they have nothing; a keyring
// that did not answer is ErrKeyringUnavailable, because "not found" is
// what callers act on (a fresh login, a logout with nothing to revoke)
// and an unread keyring may still hold a live credential.
func (Store) Load(key string) (string, error) {
	v, err := fileLoad(key)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return v, err
	}
	v, err = kr.get(keyringService, key)
	switch {
	case err == nil:
		return v, nil
	case errors.Is(err, keyring.ErrNotFound):
		return "", ErrNotFound
	}
	return "", fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
}

// Delete removes a secret from both backends. Both are attempted even
// when the first fails, and nil means both are confirmed clear: a
// logout that reports success while a copy survives is worse than one
// that says what it could not check. Missing entries are not an error;
// a keyring that answers nothing is ErrKeyringUnavailable.
func (Store) Delete(key string) error {
	kerr := keyringClear(key)
	ferr := fileDelete(key)
	if ferr != nil && kerr != nil {
		// Two failures; the file one is actionable (the message names
		// the path), so it leads, and the keyring one is demoted to text
		// so the pair does not read as "only the keyring went unchecked".
		return fmt.Errorf("%w (also: %v)", ferr, kerr)
	}
	return errors.Join(kerr, ferr)
}

// keyringClear removes key from the keyring, telling "nothing there"
// (fine) from "still there" (an error: a keyring that serves an entry
// it will not delete) from "no answer at all" (ErrKeyringUnavailable).
// A failed delete is classified by trying to read the key back, since
// go-keyring's error for a missing Secret Service, a locked collection
// and a denied prompt is platform-specific text rather than a sentinel.
func keyringClear(key string) error {
	err := kr.delete(keyringService, key)
	if err == nil || errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	if _, getErr := kr.get(keyringService, key); getErr == nil {
		return fmt.Errorf("keyring still holds %s: %w", key, err)
	} else if errors.Is(getErr, keyring.ErrNotFound) {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
}

// Migrate removes the v0.1 cookie-header slot. It reports whether one
// was there so the caller can explain the login prompt that follows. An
// unreadable keyring is not fatal here, on either side: the slot is a
// dead value being tidied away, and a run must not die on a cleanup it
// cannot finish.
func (s Store) Migrate() (bool, error) {
	if _, err := s.Load(legacyCookieKey); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrKeyringUnavailable) {
			return false, nil
		}
		return false, err
	}
	err := s.Delete(legacyCookieKey)
	if errors.Is(err, ErrKeyringUnavailable) {
		err = nil
	}
	return true, err
}

// credentialsPath returns the fallback file location, creating parents.
// The directory mode is enforced on every call, not only on creation,
// because MkdirAll leaves an existing directory's mode alone.
func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving config dir: %w", err)
	}
	dir = filepath.Join(dir, "ngchat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("securing config dir: %w", err)
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func fileRead() (map[string]string, string, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, path, nil
	}
	if err != nil {
		return nil, path, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, path, fmt.Errorf("parsing %s: %w", path, err)
	}
	return m, path, nil
}

// fileWrite replaces the credentials file with a fresh private one. An
// in-place os.WriteFile applies its mode only when it creates the file,
// so a copy restored from a backup or chmodded by hand would keep its
// wider mode, and a symlink at the path would be followed; a temp file
// (0600 from CreateTemp) renamed over the path replaces the link itself.
// On Windows there are no mode bits to enforce: the per-user config
// directory's ACL is what keeps the file private there.
func fileWrite(path string, m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credentials-*.json")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(raw)
	if err := errors.Join(werr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

func fileSave(key, value string) error {
	m, path, err := fileRead()
	if err != nil {
		return err
	}
	m[key] = value
	if err := fileWrite(path, m); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr,
		"ngchat: no OS keyring available; credential stored in %s (0600)\n",
		path)
	return nil
}

// fileDelete drops key from the fallback file. A missing file or key is
// not an error; a file that cannot be read or parsed is, since the key
// may well be inside it.
func fileDelete(key string) error {
	m, path, err := fileRead()
	if err != nil {
		return err
	}
	if _, ok := m[key]; !ok {
		return nil
	}
	delete(m, key)
	return fileWrite(path, m)
}

func fileLoad(key string) (string, error) {
	m, _, err := fileRead()
	if err != nil {
		return "", err
	}
	v, ok := m[key]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}
