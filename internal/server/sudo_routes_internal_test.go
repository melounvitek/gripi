package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A gripi sudo request notifies the session's owner at once, unless a client of theirs shows the session.
func TestSudoRequestNotifiesAtOnceUnlessTheSessionIsFocused(t *testing.T) {
	handler := newPushTestHandler(t, false, true)
	path := writeNotificationSession(t, handler.app.config.SessionsRoot, "Server setup")
	delivered := make(chan map[string]string, 10)
	handler.app.pushNotifier = pushNotifierFunc(func(_ context.Context, owner string, payload []byte) error {
		var notification map[string]string
		if err := json.Unmarshal(payload, &notification); err != nil {
			return err
		}
		notification["owner"] = owner
		delivered <- notification
		return nil
	})
	// ask runs gripi sudo until the returned function stops it, once its request shows.
	ask := func(argv ...string) (stop func()) {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://gripi/sudo", strings.NewReader(url.Values{"session": {path}, "cwd": {"/srv"}, "argv": argv}.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			handler.Local().ServeHTTP(httptest.NewRecorder(), request)
		}()
		for deadline := time.Now().Add(5 * time.Second); len(handler.app.sudoRequests.of(path)) == 0; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("gripi sudo asked nothing")
			}
		}
		return func() {
			cancel()
			<-done
		}
	}
	next := func() map[string]string {
		t.Helper()
		select {
		case notification := <-delivered:
			return notification
		case <-time.After(5 * time.Second):
			t.Fatal("no notification")
			return nil
		}
	}
	focus := func(focused bool, sequence int) {
		t.Helper()
		body := pushJSON(t, map[string]any{"client_id": "window", "session": path, "focused": focused, "sequence": sequence})
		if response := pushRequest(handler, http.MethodPut, "/web-push/presence", body, ""); response.Code != http.StatusNoContent {
			t.Fatalf("presence = %d %s", response.Code, response.Body)
		}
	}

	ask("apt-get", "install", "-y", "two words")()
	first := next()
	if first["type"] != "gripi-notification" || first["owner"] != singleUserOwner || first["title"] != "Server setup" || first["body"] != `sudo: apt-get install -y "two words"` || first["url"] != "/?session="+url.QueryEscape(path) || !strings.HasPrefix(first["tag"], "gripi-sudo:") {
		t.Fatalf("notification = %v", first)
	}
	focus(true, 1)
	ask("echo", "seen")()
	focus(false, 2)
	ask("echo", "unseen")()
	if last := next(); last["body"] != "sudo: echo unseen" || last["tag"] == first["tag"] {
		t.Fatalf("notification after one for a focused session = %v", last)
	}
}

// An answer to sudo's asking again that comes as the run ends neither reaches askpass nor stays remembered.
func TestSudoRunTakesNoAnswerThatComesAsItEnds(t *testing.T) {
	var requests sudoRequests
	run := &sudoRun{ctx: context.Background(), request: &sudoRequest{view: sudoRequestView{Session: "s"}}, delivered: true}
	given := make(chan []byte, 1)
	go func() { given <- requests.password(context.Background(), run) }()
	var views []sudoRequestView
	for deadline := time.Now().Add(5 * time.Second); len(views) == 0; time.Sleep(time.Millisecond) {
		if views = requests.of("s"); time.Now().After(deadline) {
			t.Fatal("sudo asking again shows no request")
		}
	}

	requests.end(run, true)
	if status := requests.approve(views[0].ID, func(string) bool { return true }, []byte("late"), true); status != http.StatusOK {
		t.Fatalf("answer = %d", status)
	}
	if password := <-given; password != nil {
		t.Fatalf("askpass got %q", password)
	}
	if len(requests.remembered) != 0 {
		t.Fatalf("Gripi remembers %v", requests.remembered)
	}
}
