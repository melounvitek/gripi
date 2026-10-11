package hermes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

// keyPrefix starts the key of every Hermes session, which can never be one of Pi's absolute paths.
const keyPrefix = "hermes:"

func sessionKey(id string) string { return keyPrefix + id }

// listedSession is a session as Hermes lists it.
type listedSession struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
}

func (listed listedSession) session() *agent.Session {
	updated, _ := time.Parse(time.RFC3339Nano, listed.UpdatedAt)
	_, err := os.Stat(listed.CWD)
	return &agent.Session{
		Path: sessionKey(listed.SessionID), CWD: listed.CWD, ID: listed.SessionID, DisplayName: listed.Title,
		CreatedAt: updated, ModifiedAt: updated, ConversationActivityAt: updated, FolderMissing: errors.Is(err, fs.ErrNotExist),
	}
}

// listing returns the sessions Hermes lists, most recently updated first. While Hermes can't list them, it returns
// those it last listed.
func (adapter *Adapter) listing() []listedSession {
	adapter.listMu.Lock()
	defer adapter.listMu.Unlock()
	if time.Since(adapter.listedAt) < listFresh {
		return adapter.listed
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	listed, err := adapter.list(ctx)
	adapter.listedAt = time.Now()
	if err != nil {
		if !errors.Is(err, adapter.failure) {
			log.Printf("list Hermes sessions: %v", err)
		}
		return adapter.listed
	}
	adapter.listed = listed
	return listed
}

func (adapter *Adapter) list(ctx context.Context) ([]listedSession, error) {
	conn, err := adapter.connected(ctx)
	if err != nil {
		return nil, err
	}
	var all []listedSession
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Sessions   []listedSession `json:"sessions"`
			NextCursor string          `json:"nextCursor"`
		}
		if err := conn.request(ctx, "session/list", params, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Sessions...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			return all, nil
		}
		cursor = page.NextCursor
	}
}

func (adapter *Adapter) find(key string) (listedSession, bool) {
	id, ok := strings.CutPrefix(key, keyPrefix)
	if !ok {
		return listedSession{}, false
	}
	for _, listed := range adapter.listing() {
		if listed.SessionID == id {
			return listed, true
		}
	}
	return listedSession{}, false
}

// store lists the sessions Hermes saved. Hermes keeps them in its own database, which only Hermes reads.
type store struct{ adapter *Adapter }

func (store store) Sessions() ([]*agent.Session, error) {
	listing := store.adapter.listing()
	sessions := make([]*agent.Session, 0, len(listing))
	for _, listed := range listing {
		sessions = append(sessions, listed.session())
	}
	return sessions, nil
}

func (store store) SessionsDeferringMetadata(func(string) bool) ([]*agent.Session, bool, error) {
	sessions, err := store.Sessions()
	return sessions, false, err
}

func (store store) Session(key string) (*agent.Session, bool) {
	listed, ok := store.adapter.find(key)
	if !ok {
		return nil, false
	}
	return listed.session(), true
}

func (store store) Window(string, string, bool, *int, *int) (agent.Window, error) {
	return agent.Window{}, fs.ErrNotExist
}

func (store store) Status(string) (agent.Status, error) { return agent.Status{}, nil }

func (store store) Generation(key string) string {
	if listed, ok := store.adapter.find(key); ok {
		return listed.SessionID
	}
	return ""
}

func (store store) Image(string, string, int) ([]byte, string, error) { return nil, "", fs.ErrNotExist }

func (store store) SubagentToolCallContext(string, string, []string) map[string]agent.ToolCallContext {
	return nil
}

func (store store) Exists(key string) bool {
	_, ok := store.adapter.find(key)
	return ok
}

func (store store) Canonical(key string) (string, bool) { return key, store.ValidKey(key) }

func (store store) Aliases(key string) []string {
	if !store.ValidKey(key) {
		return nil
	}
	return []string{key}
}

func (store store) ValidKey(key string) bool {
	id, ok := strings.CutPrefix(key, keyPrefix)
	return ok && id != ""
}

// PendingKey is never asked for: Hermes names a new session as it starts it.
func (store store) PendingKey() string { return "" }

func (store store) FallbackCWD(string) string { return store.adapter.home }

func (store store) ExportName(key string) string { return strings.TrimPrefix(key, keyPrefix) }

func (store store) Delete(key string) (string, error) {
	return "", fmt.Errorf("Gripi cannot delete Hermes sessions: %s", key)
}

func (store store) SaveCache() error { return nil }
