package hermes

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strconv"
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
	adapter.listedAt = time.Now()
	// connected logs why Hermes cannot start.
	conn, err := adapter.connected(ctx)
	if err != nil {
		return adapter.listed
	}
	listed, err := adapter.list(ctx, conn)
	if err != nil {
		log.Printf("list Hermes sessions: %v", err)
		return adapter.listed
	}
	adapter.listed = listed
	return listed
}

// relist has the next listing ask Hermes again, such as once it saves a session as a run starts, or renames it after.
func (adapter *Adapter) relist() {
	adapter.listMu.Lock()
	defer adapter.listMu.Unlock()
	adapter.listedAt = time.Time{}
}

func (adapter *Adapter) list(ctx context.Context, conn *connection) ([]listedSession, error) {
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
	// A recent Hermes leaves out every conversation that it files under its own platform, though it loads one by its ID.
	// The gateway finds such a conversation only if it has prompted it.
	adapter.sessionsMu.Lock()
	session := adapter.sessions[id]
	adapter.sessionsMu.Unlock()
	if session == nil {
		return listedSession{}, false
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return listedSession{SessionID: id, CWD: session.cwd}, session.prompted
}

// store lists the sessions Hermes saved. Hermes keeps them in its own database, which only Hermes reads.
type store struct{ adapter *Adapter }

func (store store) Sessions() ([]*agent.Session, error) {
	listing := store.adapter.listing()
	sessions := make([]*agent.Session, 0, len(listing))
	for _, listed := range listing {
		sessions = append(sessions, store.adapter.described(listed))
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
	return store.adapter.described(listed), true
}

// described adds what the adapter has followed of a session to what Hermes lists.
func (adapter *Adapter) described(listed listedSession) *agent.Session {
	result := listed.session()
	adapter.sessionsMu.Lock()
	session := adapter.sessions[listed.SessionID]
	adapter.sessionsMu.Unlock()
	result.AssistantResponseCount = adapter.replyCount(listed.SessionID)
	if session != nil {
		session.mu.Lock()
		result.LatestAssistantResponsePreview = agent.NotificationPreview(session.transcript.lastReply())
		session.mu.Unlock()
	}
	return result
}

// Window pages the conversation that Hermes replays as it loads the session. Hermes keeps no branches, so the leaf
// is ignored.
func (store store) Window(key, _ string, _ bool, cursor, after *int) (agent.Window, error) {
	listed, ok := store.adapter.find(key)
	if !ok {
		return agent.Window{}, fs.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	session, _, err := store.adapter.load(ctx, listed)
	if err != nil {
		return agent.Window{}, err
	}
	return window(session.messages(), cursor, after, session.status()), nil
}

// Status reports the model and context usage of a session that Hermes has loaded.
func (store store) Status(key string) (agent.Status, error) {
	id, _ := strings.CutPrefix(key, keyPrefix)
	store.adapter.sessionsMu.Lock()
	session := store.adapter.sessions[id]
	store.adapter.sessionsMu.Unlock()
	if session == nil {
		return agent.Status{}, nil
	}
	return session.status(), nil
}

// Generation is the session's ID, which Hermes gives no other session. A page that shows a session before Hermes saves
// it thus marks it read once Hermes has.
func (store store) Generation(key string) string { return strings.TrimPrefix(key, keyPrefix) }

// Image reads an image file that a reply shares, the index-th of the entry-th message.
func (store store) Image(key, entryID string, index int) ([]byte, string, error) {
	listed, ok := store.adapter.find(key)
	entry, err := strconv.Atoi(entryID)
	if !ok || err != nil {
		return nil, "", fs.ErrNotExist
	}
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()
	session, _, err := store.adapter.load(ctx, listed)
	if err != nil {
		return nil, "", err
	}
	session.mu.Lock()
	paths := session.transcript.media[entry]
	session.mu.Unlock()
	if index < 0 || index >= len(paths) {
		return nil, "", fs.ErrNotExist
	}
	return readImage(paths[index])
}

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
