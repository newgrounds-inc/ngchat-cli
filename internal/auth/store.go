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

// RememberKey is the credential-store slot for the remember cookie
// value. It is the only key a current binary writes.
const RememberKey = "ng_remember"

// legacyCookieKey held a whole Cookie header in v0.1 (pasted from a
// browser). Its session cookie is long dead and the header cannot be
// turned into a remember value, so migration deletes it and the user
// logs in once.
const legacyCookieKey = "ng_cookie"

// ErrNotFound is returned by Load when no value is stored under a key.
var ErrNotFound = errors.New("credential not found")

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

// Save stores a secret under key, preferring the keyring.
func (Store) Save(key, value string) error {
	if err := kr.set(keyringService, key, value); err == nil {
		return nil
	}
	return fileSave(key, value)
}

// Load retrieves a secret, checking the keyring then the fallback file.
// Returns ErrNotFound when neither has it.
func (Store) Load(key string) (string, error) {
	if v, err := kr.get(keyringService, key); err == nil {
		return v, nil
	}
	return fileLoad(key)
}

// Delete removes a secret from both backends; missing entries are not an
// error.
func (Store) Delete(key string) error {
	_ = kr.delete(keyringService, key)
	m, path, err := fileRead()
	if err != nil || m == nil {
		return nil
	}
	delete(m, key)
	return fileWrite(path, m)
}

// Migrate removes the v0.1 cookie-header slot. It reports whether one
// was there so the caller can explain the login prompt that follows.
func (s Store) Migrate() (bool, error) {
	if _, err := s.Load(legacyCookieKey); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, s.Delete(legacyCookieKey)
}

// credentialsPath returns the fallback file location, creating parents.
func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolving config dir: %w", err)
	}
	dir = filepath.Join(dir, "ngchat")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating config dir: %w", err)
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

func fileWrite(path string, m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
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
