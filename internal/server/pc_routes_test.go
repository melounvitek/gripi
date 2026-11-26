package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/config"
)

// The desktop app drops a link whose poll carries more than 64 messages, and reads a poll's JSON only up to 8 MiB, so
// a poll carries at most 64 messages and 2 MiB of agent messages, and the others wait for the next one.
func TestPCLinkPollCarriesAtMost64MessagesAnd2MiBOfAgentMessages(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	for _, queued := range []struct {
		count, size int
		polls       []int
	}{{65, 1, []int{64, 1}}, {9, pcMessageLimit, []int{8, 1}}} {
		links := &pcLinks{}
		if _, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app", Open: true}); err != nil {
			t.Fatal(err)
		}
		links.mu.Lock()
		for range queued.count {
			links.byID["app"].send(pcMessage{Type: "ssh", Message: make([]byte, queued.size)})
		}
		links.mu.Unlock()

		for _, want := range queued.polls {
			messages, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app"})
			if err != nil || len(messages) != want {
				t.Fatalf("poll of %d messages of %d bytes = %d messages (%v), want %d", queued.count, queued.size, len(messages), err, want)
			}
		}
	}
}

// Gripi keeps nothing of a scratch session, so not where it was used either.
func TestAUseOfAScratchSessionIsNeverRecorded(t *testing.T) {
	app := &application{
		config:          config.Config{AdminPassword: "secret"},
		pendingSessions: agent.NewPendingSessionRegistry(time.Now),
		scratch:         scratchSessions{byOwner: map[string]*scratchSession{"": {key: "scratch"}}, ended: map[string]bool{}},
	}
	request := httptest.NewRequest("PUT", "/web-push/presence", nil)
	request.Header.Set("Cookie", "gripi_browser=token")

	app.usedPC(request, "scratch")
	if len(app.pcLinks.uses) != 0 {
		t.Fatalf("uses = %v", app.pcLinks.uses)
	}
}

// A new session has a pending key until Pi saves it. Its use moves with it then, and a page that still has the pending
// key reports a use of the saved one.
func TestAUseOfANewSessionFollowsItWhenPiSavesIt(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	root := t.TempDir()
	app := notificationTestApplication(t, root, false, false, nil)
	pending, saved := filepath.Join(root, "pending.jsonl"), filepath.Join(root, "session.jsonl")
	if err := app.rpcClients.Register(pending, &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": saved}}}); err != nil {
		t.Fatal(err)
	}
	app.pendingSessions.Remember(pending, root, "")
	used := func(token, session string) *http.Request {
		request := httptest.NewRequest(http.MethodPut, "http://example.com/web-push/presence", strings.NewReader(`{"client_id":"`+token+`","session":"`+session+`","focused":true,"used":true,"sequence":1}`))
		request.Header.Set("Cookie", "gripi_browser="+token)
		owner, _ := app.pushOwner(request)
		if _, err := app.pcLinks.poll(context.Background(), owner, pcLinkBody{Link: token, Open: true}); err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		app.updateNotificationPresence(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("presence = %d %s", response.Code, response.Body)
		}
		return request
	}
	// As the PC routes find the session, which moves it once Pi saved it.
	routesTo := func(request *http.Request, connection string) string {
		session, err := app.canonicalRPCSessionPath(request, saved)
		if err != nil {
			t.Fatal(err)
		}
		link, _, err := app.pcLinks.route(nil, session, connection, true)
		if err != nil {
			t.Fatal(err)
		}
		return link.id
	}

	request := used("laptop", pending)
	writeNotificationSession(t, root, "New session")
	if link := routesTo(request, "first"); link != "laptop" {
		t.Fatalf("the saved session routes to %s", link)
	}
	if resolved, remapped := app.pendingSessions.Resolve(pending); !remapped || resolved != saved {
		t.Fatalf("the pending session resolves to %q, %v", resolved, remapped)
	}
	if link := routesTo(used("desk", pending), "second"); link != "desk" {
		t.Fatalf("after a use under the pending key, the saved session routes to %s", link)
	}
}
