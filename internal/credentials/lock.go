package credentials

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Refresh tokens are single-use: the server revokes the whole connection when
// one is presented twice. Every CLI process refreshing the same account takes
// this lock, re-reads the stored tokens inside it, and only then refreshes, so
// two processes never spend the same refresh token. A lock file (created with
// O_EXCL) works the same on macOS, Linux and Windows.
const (
	lockWait  = 30 * time.Second
	lockStale = 60 * time.Second
)

// WithAccountLock runs fn while holding the refresh lock for one account.
func (s *Store) WithAccountLock(apiBase, kind, id string, fn func() error) error {
	sum := sha256.Sum256([]byte(Key(apiBase, kind, id)))
	path := filepath.Join(s.dir, "refresh-"+hex.EncodeToString(sum[:8])+".lock")
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("lock credentials: %w", err)
		}
		// A process that died while refreshing leaves its lock behind.
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > lockStale {
			os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("another tringify command is refreshing the sign-in; try again")
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer os.Remove(path)
	return fn()
}
