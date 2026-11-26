package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The desktop app drops a link whose poll carries more than 64 messages, and reads a poll's JSON only up to 8 MiB, so
// a poll carries at most 64 messages and 4 MiB of their JSON, but for a single message, and the others wait for the
// next one.
func TestPCLinkPollCarriesAtMost64MessagesAnd4MiBOfTheirJSON(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	// The longest command that the gateway takes, of words that JSON escapes most.
	argv := make([]string, pcRunWords)
	for index := range argv {
		argv[index] = strings.Repeat("<", pcRunWordBytes)
	}
	for _, queued := range []struct {
		name    string
		count   int
		message pcMessage
		polls   []int
	}{
		{"small messages", 65, pcMessage{Type: "ssh", Message: make([]byte, 1)}, []int{64, 1}},
		{"agent messages of 256 KiB", 13, pcMessage{Type: "ssh", Message: make([]byte, pcMessageLimit)}, []int{11, 2}},
		{"the longest commands", 2, pcMessage{Type: "run", Argv: argv, CWD: "/" + strings.Repeat("<", pcRunWordBytes-1)}, []int{1, 1}},
	} {
		links := &pcLinks{}
		if _, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app", Open: true}); err != nil {
			t.Fatal(err)
		}
		links.mu.Lock()
		for range queued.count {
			links.byID["app"].send(queued.message)
		}
		links.mu.Unlock()

		for _, want := range queued.polls {
			messages, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app"})
			encoded, _ := json.Marshal(map[string][]pcMessage{"messages": messages})
			if err != nil || len(messages) != want || len(encoded) > 8<<20 {
				t.Fatalf("poll of %d %s = %d messages in %d bytes (%v), want %d", queued.count, queued.name, len(messages), len(encoded), err, want)
			}
		}
	}
}

// The desktop app runs a command only once the gateway says that gripi pc run still waits for it, so the gateway says
// no once gripi's request ended or timed out, also before the gateway took the request away, and for other requests.
func TestACommandStartsOnlyWhileGripiWaitsForIt(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	links := &pcLinks{}
	if _, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app", Open: true}); err != nil {
		t.Fatal(err)
	}
	ended, end := context.WithCancel(context.Background())
	end()
	timedOut := time.NewTimer(0)
	<-timedOut.C
	for _, waiter := range []struct {
		name   string
		ctx    context.Context
		kind   string
		timer  *time.Timer
		status int
	}{
		{"a command", context.Background(), "run", time.NewTimer(time.Minute), http.StatusOK},
		{"an SSH request", context.Background(), "ssh", time.NewTimer(time.Minute), http.StatusNotFound},
		{"a command whose gripi ended", ended, "run", time.NewTimer(time.Minute), http.StatusNotFound},
		{"a command that timed out", context.Background(), "run", timedOut, http.StatusNotFound},
	} {
		links.mu.Lock()
		links.byID["app"].waiting["request"] = &pcWaiter{ctx: waiter.ctx, timeout: waiter.timer, approval: pcApproval{kind: waiter.kind}}
		links.mu.Unlock()
		if status, _ := links.status("owner", pcLinkBody{Link: "app", ID: "request", Starting: true}); status != waiter.status {
			t.Fatalf("starting %s = %d, want %d", waiter.name, status, waiter.status)
		}
	}
}

// Gripi keeps nothing of a scratch session, so not where it was used either.
func TestAUseOfAScratchSessionIsNeverRecorded(t *testing.T) {
	root := t.TempDir()
	app := notificationTestApplication(t, root, false, false, nil)
	scratch := filepath.Join(root, "scratch.jsonl")
	app.scratch = scratchSessions{byOwner: map[string]*scratchSession{"": {key: scratch}}, ended: map[string]bool{}}
	app.pendingSessions.Remember(scratch, root, "")
	request := httptest.NewRequest(http.MethodPut, "http://example.com/web-push/presence", nil)
	request.Header.Set("Cookie", "gripi_browser=token")

	app.usedPC(request, scratch, time.Now())
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
		link, _, err := app.pcLinks.route(nil, session, connection, true, nil)
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

// The page retries a message that the gateway asks it to, such as while a new session's Pi stops for being idle, and its
// retries don't count, so the first attempt counts before the gateway finds the session.
func TestAMessageThatThePageRetriesCountsAtOnce(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	root := t.TempDir()
	app := notificationTestApplication(t, root, false, false, nil)
	pending, saved := filepath.Join(root, "pending.jsonl"), writeNotificationSession(t, root, "New session")
	if err := app.rpcClients.Register(pending, &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": saved}}}); err != nil {
		t.Fatal(err)
	}
	app.pendingSessions.Remember(pending, root, "")
	release, retiring := app.promptAdmissions.retireIdle(pending)
	if !retiring {
		t.Fatal("the new session's Pi is not stopping")
	}

	request := httptest.NewRequest(http.MethodPost, "http://example.com/prompt", strings.NewReader("session="+pending+"&message=hello"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Cookie", "gripi_browser=laptop")
	response := httptest.NewRecorder()
	app.prompt(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "session_operation_pending") {
		t.Fatalf("prompt = %d %s", response.Code, response.Body)
	}
	release()

	owner, _ := app.pushOwner(request)
	if _, err := app.pcLinks.poll(context.Background(), owner, pcLinkBody{Link: "laptop", Open: true}); err != nil {
		t.Fatal(err)
	}
	session, err := app.canonicalRPCSessionPath(request, saved)
	if err != nil {
		t.Fatal(err)
	}
	if link, _, err := app.pcLinks.route(nil, session, "connection", true, nil); err != nil || link.id != "laptop" {
		t.Fatalf("the session routes to %v (%v)", link, err)
	}
}

// gripi closes an agent connection once its request ended, and the close may overtake the request on its way to the
// link, which mustn't open the connection again there.
func TestARequestOfAClosedAgentConnectionNeverReachesTheLink(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	links := &pcLinks{}
	if _, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app", Open: true}); err != nil {
		t.Fatal(err)
	}
	links.use("owner", time.Now(), func() string { return "session" })
	link, session, err := links.route(nil, "session", "connection", true, nil)
	if err != nil {
		t.Fatal(err)
	}

	links.closeConnection("connection")
	if _, err := links.ask(context.Background(), link, func() string { return session }, pcMessage{Type: "ssh", ID: "request", Session: session, Connection: "connection"}); err != errPCClosed {
		t.Fatalf("ask = %v", err)
	}
	if messages, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app"}); err != nil || len(messages) != 1 || messages[0].Type != "ssh_close" {
		t.Fatalf("the link got %+v (%v)", messages, err)
	}
}

// A computer that begins to ask the user to approve a request notifies their other browsers at once and once, also
// while they show the session: all clients are one for presence, also the computer that asks, which comes to the front
// with a notification of its own. A command shows on a locked screen, so only that it asks to run one does.
func TestAComputerThatAsksToApproveNotifiesTheUsersOtherBrowsersOnce(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	for _, request := range []struct{ route, form, summary, body string }{
		{"/pc/ssh-agent", "connection=c&message=Cw%3D%3D&open=1", "SSH login to github.com as git", "Approve on laptop: SSH login to github.com as git"},
		{"/pc/run", "argv=cat&argv=.env", "/usr/bin/cat .env", "Approve on laptop: run a command"},
	} {
		t.Run(request.route, func(t *testing.T) { notifiesOnce(t, request.route, request.form, request.summary, request.body) })
	}
}

func notifiesOnce(t *testing.T, route, form, summary, body string) {
	handler := newPushTestHandler(t, false, false)
	app := handler.app
	path := writeNotificationSession(t, app.config.SessionsRoot, "Fix login redirect")
	delivered := make(chan map[string]any, 10)
	app.pushNotifier = pushNotifierFunc(func(_ context.Context, owner string, payload []byte) error {
		var notification map[string]any
		if err := json.Unmarshal(payload, &notification); err != nil {
			return err
		}
		notification["owner"] = owner
		delivered <- notification
		return nil
	})
	owners := map[string]string{}
	for _, token := range []string{"laptop", "phone"} {
		if _, err := app.browserStore.ApproveCurrentBrowser(token, token); err != nil {
			t.Fatal(err)
		}
		owners[token], _ = app.pushOwner(requestWithCookie("gripi_browser=" + token))
		if err := app.pushSubscriptions.Upsert(owners[token], pushRouteSubscription(token)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.pcLinks.poll(context.Background(), owners["laptop"], pcLinkBody{Link: "app", Open: true, Name: "laptop", Run: true}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"laptop", "phone"} {
		presence := pushJSON(t, map[string]any{"client_id": token, "session": path, "focused": true, "used": token == "laptop", "sequence": 1})
		if response := pushRequest(handler, http.MethodPut, "/web-push/presence", presence, "gripi_browser="+token); response.Code != http.StatusNoContent {
			t.Fatalf("presence = %d %s", response.Code, response.Body)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://gripi"+route, strings.NewReader(form+"&session="+url.QueryEscape(path)))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		handler.Local().ServeHTTP(httptest.NewRecorder(), request)
	}()
	var id string
	for deadline := time.Now().Add(5 * time.Second); id == ""; {
		messages, _ := app.pcLinks.poll(context.Background(), owners["laptop"], pcLinkBody{Link: "app"})
		for _, message := range messages {
			id = message.ID
		}
		if time.Now().After(deadline) {
			t.Fatal("the link got no request")
		}
	}
	status := func(asking bool) {
		t.Helper()
		status := pushJSON(t, map[string]any{"link": "app", "id": id, "asking": asking, "summary": summary, "expires_in": 120})
		request := httptest.NewRequest(http.MethodPost, "http://example.com/pc/link/status", strings.NewReader(status))
		request.RemoteAddr = "127.0.0.1:40000"
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gripi-PC-Link", "1")
		request.Header.Set("Cookie", "gripi_browser=laptop")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d %s", response.Code, response.Body)
		}
	}

	status(true)
	select {
	case notification := <-delivered:
		if notification["owner"] != owners["phone"] || notification["title"] != "Fix login redirect" || notification["body"] != body || notification["plain"] != true || notification["url"] != "/?session="+url.QueryEscape(path) || notification["session"] != path || !strings.HasPrefix(notification["tag"].(string), "gripi-pc:") {
			t.Fatalf("notification = %v", notification)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification")
	}
	status(true)
	status(false)
	status(true)
	select {
	case notification := <-delivered:
		t.Fatalf("another notification = %v", notification)
	case <-time.After(200 * time.Millisecond):
	}
}

// A new session has a pending key until Pi saves it, and a request that a computer asks to approve in it moves with it,
// as the session's page and live events look it up by the saved key, also when Pi saves it before the request waits.
func TestARequestThatAComputerAsksToApproveFollowsANewSessionWhenPiSavesIt(t *testing.T) {
	poll := PCLinkPoll
	PCLinkPoll = 10 * time.Millisecond
	t.Cleanup(func() { PCLinkPoll = poll })
	for _, early := range []bool{false, true} {
		root := t.TempDir()
		app := notificationTestApplication(t, root, false, false, nil)
		pending, saved := filepath.Join(root, "pending.jsonl"), filepath.Join(root, "session.jsonl")
		if err := app.rpcClients.Register(pending, &remapClient{state: map[string]any{"data": map[string]any{"sessionFile": saved}}}); err != nil {
			t.Fatal(err)
		}
		app.pendingSessions.Remember(pending, root, "")
		links := &app.pcLinks
		if _, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app", Open: true}); err != nil {
			t.Fatal(err)
		}
		links.use("owner", time.Now(), func() string { return pending })
		link, key, err := links.route(nil, pending, "connection", true, nil)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
		save := func() {
			writeNotificationSession(t, root, "New session")
			if session, err := app.canonicalRPCSessionPath(request, pending); err != nil || session != saved {
				t.Fatalf("the pending session is %q (%v)", session, err)
			}
		}
		if early {
			save()
		}
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		resolve := func() string {
			resolved, _, _ := app.resolveOwnedPendingPath(request, pending)
			return resolved
		}
		go links.ask(ctx, link, resolve, pcMessage{Type: "ssh", ID: "request", Session: key, Connection: "connection"})
		if messages, err := links.poll(context.Background(), "owner", pcLinkBody{Link: "app"}); err != nil || len(messages) != 1 || messages[0].Session != pending {
			t.Fatalf("the link got %+v (%v)", messages, err)
		}
		if status, _ := links.status("owner", pcLinkBody{Link: "app", ID: "request", Asking: true, Summary: "SSH login", ExpiresIn: 120}); status != http.StatusOK {
			t.Fatalf("status = %d", status)
		}
		if !early {
			save()
		}
		if approvals := links.approvals(saved); len(approvals) != 1 || approvals[0].ID != "request" {
			t.Fatalf("with Pi saving the session early %v, the saved session shows %+v", early, approvals)
		}
	}
}

// A Web Push message carries at most about 4 KiB, so a notification names its session only when it stays well within
// that, which JSON's escapes can take a long session key or body over.
func TestANotificationAtOnceNamesItsSessionOnlyWithinThePushLimit(t *testing.T) {
	root := t.TempDir()
	notifier := &recordingPushNotifier{}
	app := notificationTestApplication(t, root, false, false, notifier)
	short, long := filepath.Join(root, "session.jsonl"), filepath.Join(root, strings.Repeat("&", 200), "session.jsonl")

	app.notifyAtOnce([]string{"owner"}, short, "request", "sudo", "sudo: true")
	app.notifyAtOnce([]string{"owner"}, long, "request", "sudo", "sudo: "+strings.Repeat("<", 400))
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		notifier.mu.Lock()
		count := len(notifier.payloads)
		notifier.mu.Unlock()
		if count == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d notifications", count)
		}
	}
	sessions := map[string]bool{}
	for _, payload := range notifier.payloads {
		var notification map[string]any
		if err := json.Unmarshal(payload, &notification); err != nil || len(payload) > 3<<10 {
			t.Fatalf("a notification of %d bytes: %s (%v)", len(payload), payload, err)
		}
		session, _ := notification["session"].(string)
		sessions[session] = true
	}
	if !reflect.DeepEqual(sessions, map[string]bool{short: true, "": true}) {
		t.Fatalf("the notifications name %v", sessions)
	}
}
