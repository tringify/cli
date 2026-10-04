// Package credentials keeps OAuth tokens for signed-in accounts.
//
// Tokens go to the operating system's credential store (macOS Keychain,
// Windows Credential Manager, or the Secret Service on Linux) when one is
// available. Otherwise they are written to a file readable only by the
// current user. Non-secret account details (which store or organization a
// login belongs to) live in a separate index file.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

// Open chooses the credential backend. TRINGIFY_CREDENTIALS_STORE=file forces
// the file backend; otherwise the system keychain is used when it works.
func Open() (*Store, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	if os.Getenv("TRINGIFY_CREDENTIALS_STORE") != "file" && keyringWorks() {
		s.backend = keyringBackend{}
	} else {
		s.backend = &fileBackend{path: filepath.Join(dir, "credentials.json")}
	}
	return s, nil
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

func keyringWorks() bool {
	// A lookup of an absent item succeeds with ErrNotFound only when a
	// credential store is actually reachable.
	_, err := keyring.Get(keyringService, "availability-check")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
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
	v, err := keyring.Get(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errNotFound
	}
	return v, err
}
func (keyringBackend) set(key, value string) error { return keyring.Set(keyringService, key, value) }
func (keyringBackend) delete(key string) error {
	err := keyring.Delete(keyringService, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return errNotFound
	}
	return err
}

type fileBackend struct{ path string }

func (f *fileBackend) name() string { return f.path }

func (f *fileBackend) read() (map[string]string, error) {
	info, err := os.Stat(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	// Windows has no POSIX modes; the file lives in the user's profile.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users; run chmod 600 on it", f.path)
	}
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fileBackend) get(key string) (string, error) {
	all, err := f.read()
	if err != nil {
		return "", err
	}
	v, ok := all[key]
	if !ok {
		return "", errNotFound
	}
	return v, nil
}

func (f *fileBackend) set(key, value string) error {
	all, err := f.read()
	if err != nil {
		return err
	}
	all[key] = value
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(f.path, raw)
}

func (f *fileBackend) delete(key string) error {
	all, err := f.read()
	if err != nil {
		return err
	}
	if _, ok := all[key]; !ok {
		return errNotFound
	}
	delete(all, key)
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(f.path, raw)
}
