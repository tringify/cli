// Package credentials keeps OAuth tokens for signed-in accounts.
//
// Tokens go only to the operating system's credential store (macOS Keychain,
// Windows Credential Manager, or the Secret Service on Linux). Non-secret
// account details (which store or organization a login belongs to) live in a
// separate index file.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const keyringService = "tringify-cli"

// Target is the store or developer organization a login is bound to.
type Target struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Account is one signed-in connection.
type Account struct {
	APIBase      string    `json:"api_base"`
	Resource     string    `json:"resource"`
	ClientID     string    `json:"client_id"`
	Target       Target    `json:"target"`
	Scope        string    `json:"scope"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// Key identifies an account: one developer organization login and any number
// of store logins per API host.
func Key(apiBase, kind, id string) string {
	if kind == "organization" {
		return apiBase + "|organization"
	}
	return apiBase + "|store:" + id
}

// Store reads and writes accounts.
type Store struct {
	dir     string
	backend backend
	mu      sync.Mutex
}

type backend interface {
	get(key string) (string, error)
	set(key, value string) error
	delete(key string) error
	name() string
}

var errNotFound = errors.New("not found")

// Open uses the operating system's credential store: the macOS Keychain,
// Windows Credential Manager, or the Secret Service on Linux. Tokens are kept
// nowhere else.
func Open() (*Store, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := keyringWorks(); err != nil {
		return nil, err
	}
	return &Store{dir: dir, backend: keyringBackend{}}, nil
}

// ConfigDir is ~/.config/tringify (or the platform equivalent), overridable
// with TRINGIFY_CONFIG_DIR.
func ConfigDir() (string, error) {
	if dir := os.Getenv("TRINGIFY_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "tringify"), nil
}

// Backend names where secrets are kept, for messages.
func (s *Store) Backend() string { return s.backend.name() }

func keyringWorks() error {
	// A lookup of an absent item succeeds with ErrNotFound only when a
	// credential store is actually reachable.
	err := keychainCall(func() error {
		_, err := keyring.Get(keyringService, "availability-check")
		return err
	})
	switch {
	case err == nil, errors.Is(err, keyring.ErrNotFound):
		return nil
	case errors.Is(err, errKeychainTimeout):
		return err
	}
	return errors.New("no system keychain is available. The Tringify CLI keeps logins in the macOS Keychain, Windows Credential Manager, or the Secret Service on Linux (such as GNOME Keyring or KWallet); start one and try again")
}

const (
	// keychainNotice is how long a keychain call runs before the CLI says it
	// is waiting: the keychain may be showing an unlock or access prompt.
	keychainNotice = 3 * time.Second
	// keychainTimeout bounds every keychain call. A credential store that
	// is installed but not answering (a Secret Service with no session to
	// show its unlock prompt, for example) would otherwise block forever.
	keychainTimeout = 2 * time.Minute
)

var errKeychainTimeout = fmt.Errorf("the system keychain did not answer within %s. If it is locked, unlock it and try again. Over SSH or in a session without a desktop, the keychain cannot show its unlock prompt: unlock it from a desktop session first (on Linux, for example with gnome-keyring-daemon --unlock)", keychainTimeout)

// keychainCall runs one keychain operation, saying on standard error when it
// is waiting for the keychain and failing once keychainTimeout passes.
func keychainCall(fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	notice := time.NewTimer(keychainNotice)
	defer notice.Stop()
	timeout := time.NewTimer(keychainTimeout)
	defer timeout.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-notice.C:
			fmt.Fprintln(os.Stderr, "Waiting for the system keychain. If it asks you to unlock it or allow access, do so.")
		case <-timeout.C:
			return errKeychainTimeout
		}
	}
}

// Save stores the account's tokens in the backend and its details in the index.
func (s *Store) Save(a Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(a.APIBase, a.Target.Kind, a.Target.ID)
	secret, err := json.Marshal(struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}{a.AccessToken, a.RefreshToken})
	if err != nil {
		return err
	}
	if err := s.backend.set(key, string(secret)); err != nil {
		return fmt.Errorf("save credentials in %s: %w", s.backend.name(), err)
	}
	index, err := s.readIndex()
	if err != nil {
		return err
	}
	a.AccessToken, a.RefreshToken = "", ""
	index[key] = a
	return s.writeIndex(index)
}

// Load returns the account with its tokens.
func (s *Store) Load(apiBase, kind, id string) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(apiBase, kind, id)
	index, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	a, ok := index[key]
	if !ok {
		return nil, nil
	}
	raw, err := s.backend.get(key)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials from %s: %w", s.backend.name(), err)
	}
	var secret struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return nil, err
	}
	a.AccessToken, a.RefreshToken = secret.AccessToken, secret.RefreshToken
	return &a, nil
}

// List returns every account for apiBase, without tokens, sorted by key.
func (s *Store) List(apiBase string) ([]Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(index))
	for k := range index {
		if strings.HasPrefix(k, apiBase+"|") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]Account, 0, len(keys))
	for _, k := range keys {
		out = append(out, index[k])
	}
	return out, nil
}

// Delete removes the account's tokens and details.
func (s *Store) Delete(apiBase, kind, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := Key(apiBase, kind, id)
	if err := s.backend.delete(key); err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	index, err := s.readIndex()
	if err != nil {
		return err
	}
	delete(index, key)
	return s.writeIndex(index)
}

func (s *Store) indexPath() string { return filepath.Join(s.dir, "accounts.json") }

func (s *Store) readIndex() (map[string]Account, error) {
	raw, err := os.ReadFile(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Account{}, nil
	}
	if err != nil {
		return nil, err
	}
	index := map[string]Account{}
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.indexPath(), err)
	}
	return index, nil
}

func (s *Store) writeIndex(index map[string]Account) error {
	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(s.indexPath(), raw)
}

// writePrivate replaces path atomically with a file only the owner can read.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tringify-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

type keyringBackend struct{}

func (keyringBackend) name() string { return "the system keychain" }
func (keyringBackend) get(key string) (string, error) {
	value := make(chan string, 1)
	err := keychainCall(func() error {
		v, err := keyring.Get(keyringService, key)
		value <- v
		return err
	})
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	return <-value, nil
}
func (keyringBackend) set(key, value string) error {
	return keychainCall(func() error { return keyring.Set(keyringService, key, value) })
}
func (keyringBackend) delete(key string) error {
	err := keychainCall(func() error { return keyring.Delete(keyringService, key) })
	if errors.Is(err, keyring.ErrNotFound) {
		return errNotFound
	}
	return err
}
