package pi

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/agent/pi/sessionfile"
)

// sessionStore reads Pi's session files. A key is the absolute path of a session file, and the canonical key goes
// through the configured sessions root even when that root is a symlink.
type sessionStore struct {
	sessionfile.Store
}

var _ agent.Store = sessionStore{}

func (store sessionStore) Exists(key string) bool {
	_, err := os.Stat(key)
	return err == nil
}

func (store sessionStore) Canonical(key string) (string, bool) {
	return sessionfile.ConfiguredSessionPath(store.Root, key)
}

func (store sessionStore) Aliases(key string) []string {
	return sessionfile.SessionPathAliases(store.Root, key)
}

func (store sessionStore) ValidKey(key string) bool {
	return filepath.IsAbs(key) && filepath.Clean(key) == key
}

// PendingKey is a path under the sessions root that stands in until Pi reports its file, which it saves only after
// the first reply.
func (store sessionStore) PendingKey() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return filepath.Join(store.Root, fmt.Sprintf("pending-%x.jsonl", time.Now().UnixNano()))
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return filepath.Join(store.Root, fmt.Sprintf("pending-%x-%x-%x-%x-%x.jsonl", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]))
}

// FallbackCWD is the folder that holds the session file.
func (store sessionStore) FallbackCWD(key string) string {
	return filepath.Dir(key)
}

// ExportName follows Pi CLI, which names an export after the session file.
func (store sessionStore) ExportName(key string) string {
	base := strings.TrimSuffix(filepath.Base(key), filepath.Ext(key))
	if base == "" || base == "." {
		base = "session"
	}
	return "pi-session-" + base
}

func (store sessionStore) Delete(key string) (string, error) {
	method, err := sessionfile.DeleteSessionFile(key)
	if err != nil {
		return "", err
	}
	store.Cache.Forget(key)
	return method, nil
}

func (store sessionStore) SaveCache() error {
	return store.Cache.Save()
}
