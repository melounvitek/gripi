package pi

import (
	"os"
	"path/filepath"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/sessions"
)

// sessionStore reads Pi's session files. A key is the absolute path of a session file, and the canonical key goes
// through the configured sessions root even when that root is a symlink.
type sessionStore struct {
	sessions.Store
}

var _ agent.Store = sessionStore{}

func (store sessionStore) Exists(key string) bool {
	_, err := os.Stat(key)
	return err == nil
}

func (store sessionStore) Canonical(key string) (string, bool) {
	return sessions.ConfiguredSessionPath(store.Root, key)
}

func (store sessionStore) Aliases(key string) []string {
	return sessions.SessionPathAliases(store.Root, key)
}

func (store sessionStore) ValidKey(key string) bool {
	return filepath.IsAbs(key) && filepath.Clean(key) == key
}

// FallbackCWD is the folder that holds the session file.
func (store sessionStore) FallbackCWD(key string) string {
	return filepath.Dir(key)
}

func (store sessionStore) Delete(key string) (string, error) {
	method, err := sessions.DeleteSessionFile(key)
	if err != nil {
		return "", err
	}
	store.Cache.Forget(key)
	return method, nil
}

func (store sessionStore) SaveCache() error {
	return store.Cache.Save()
}
