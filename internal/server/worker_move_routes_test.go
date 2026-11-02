package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/agent"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// fakeWorkers moves sessions of the repository in one folder to the worker beelink, and forwards ports there, as far
// as the gateway sees it.
type fakeWorkers struct {
	repository string
	mu         sync.Mutex
	placed     map[string]bool
	moving     map[string]time.Time
	moves      []string
	forwards   map[int]int
	notices    []gateway.WorkerNotice
	// snapshot, if set, runs while the other sessions of the repository are held, and copying once they are released.
	snapshot, copying func()
	// copyFails, if set, is what a move's copy fails with.
	copyFails error
}

// refusal is an error of Workers that refuses what was asked.
type refusal struct{ error }

func (refusal) Refused() bool { return true }

func (workers *fakeWorkers) inRepository(folder string) bool {
	return folder == workers.repository || strings.HasPrefix(folder, workers.repository+"/")
}

func (workers *fakeWorkers) onWorker(key string) bool {
	workers.mu.Lock()
	defer workers.mu.Unlock()
	return workers.placed[key]
}

// Session tells the gripi commands of beelink which sessions run there.
func (workers *fakeWorkers) Session(key string) bool {
	return workers.onWorker(key)
}

func (workers *fakeWorkers) Placed() map[string]string {
	workers.mu.Lock()
	defer workers.mu.Unlock()
	placed := map[string]string{}
	for key, onWorker := range workers.placed {
		if onWorker {
			placed[key] = workers.repository
		}
	}
	return placed
}

func (workers *fakeWorkers) move(ctx context.Context, key, record string, prepare func(context.Context, func(string) bool) (func(), error), placed bool) error {
	workers.mu.Lock()
	if workers.moving == nil {
		workers.moving, workers.placed = map[string]time.Time{}, map[string]bool{}
	}
	workers.moving[key] = time.Now()
	workers.mu.Unlock()
	defer func() {
		workers.mu.Lock()
		delete(workers.moving, key)
		workers.mu.Unlock()
	}()
	release, err := prepare(ctx, workers.inRepository)
	if err != nil {
		return err
	}
	if workers.snapshot != nil {
		workers.snapshot()
	}
	release()
	if workers.copying != nil {
		workers.copying()
	}
	if workers.copyFails != nil {
		return workers.copyFails
	}
	workers.mu.Lock()
	defer workers.mu.Unlock()
	workers.moves = append(workers.moves, record)
	workers.placed[key] = placed
	return nil
}

func (workers *fakeWorkers) Move(ctx context.Context, key, cwd, worker string, prepare func(context.Context, func(string) bool) (func(), error)) error {
	if !workers.inRepository(cwd) {
		return refusal{errors.New(cwd + " is not in a Git repository")}
	}
	if worker != "beelink" {
		return errors.New("ssh: Could not resolve hostname " + worker)
	}
	if workers.onWorker(key) {
		return refusal{errors.New("the session is on worker beelink already")}
	}
	return workers.move(ctx, key, "to "+worker, prepare, true)
}

func (workers *fakeWorkers) Return(ctx context.Context, key string, prepare func(context.Context, func(string) bool) (func(), error)) error {
	if !workers.onWorker(key) {
		return refusal{errors.New("the session is not on a worker")}
	}
	return workers.move(ctx, key, "back", prepare, false)
}

func (workers *fakeWorkers) Forward(_ context.Context, key string, port int) (int, error) {
	if !workers.onWorker(key) {
		return 0, refusal{errors.New("the session is not on a worker")}
	}
	workers.mu.Lock()
	defer workers.mu.Unlock()
	if workers.forwards == nil {
		workers.forwards = map[int]int{}
	}
	workers.forwards[port] = port + 1
	return port + 1, nil
}

func (workers *fakeWorkers) Unforward(key string, port int) error {
	workers.mu.Lock()
	defer workers.mu.Unlock()
	if _, found := workers.forwards[port]; !found || !workers.placed[key] {
		return refusal{errors.New("the port is not forwarded")}
	}
	delete(workers.forwards, port)
	return nil
}

func (workers *fakeWorkers) List() ([]gateway.Worker, error) {
	return []gateway.Worker{{Name: "beelink", Destination: "gripi@beelink", State: "connected"}, {Name: "nuc", Destination: "gripi@nuc", State: "unreachable"}}, nil
}

func (workers *fakeWorkers) Location(key string) (gateway.WorkerLocation, bool) {
	workers.mu.Lock()
	defer workers.mu.Unlock()
	since, moving := workers.moving[key]
	if !workers.placed[key] && !moving {
		return gateway.WorkerLocation{}, false
	}
	location := gateway.WorkerLocation{Worker: "beelink", State: "connected", Repository: workers.repository, Copying: since, Ports: []gateway.WorkerPort{}}
	for port, local := range workers.forwards {
		location.Ports = append(location.Ports, gateway.WorkerPort{Port: port, Local: local})
	}
	return location, true
}

func (workers *fakeWorkers) Selection(_, cwd string) (string, []string, error) {
	if !workers.inRepository(cwd) {
		return "", nil, refusal{errors.New(cwd + " is not in a Git repository")}
	}
	return workers.repository, []string{workers.repository, workers.repository + "-feature"}, nil
}

func (workers *fakeWorkers) Notices(string) []gateway.WorkerNotice {
	workers.mu.Lock()
	defer workers.mu.Unlock()
	return workers.notices
}

func (workers *fakeWorkers) DismissNotices(context.Context, string) error {
	workers.mu.Lock()
	defer workers.mu.Unlock()
	workers.notices = nil
	return nil
}

// fakePiPIDs returns the fake Pi's processes that started for a session, and those of them that stopped.
func fakePiPIDs(t *testing.T, session string) (started, stopped []float64) {
	t.Helper()
	contents, _ := os.ReadFile(os.Getenv("GRIPI_E2E_FAKE_PI_LOG"))
	var pids []float64
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		var record struct {
			PID         float64 `json:"pid"`
			Event       string  `json:"event"`
			SessionPath string  `json:"sessionPath"`
		}
		json.Unmarshal([]byte(line), &record)
		switch {
		case record.Event == "started" && record.SessionPath == session:
			started = append(started, record.PID)
			pids = append(pids, record.PID)
		case record.Event == "stopped" && slices.Contains(pids, record.PID):
			stopped = append(stopped, record.PID)
		}
	}
	return started, stopped
}

// workerOf returns where a session runs as its page's poll sees it.
func workerOf(t *testing.T, handler http.Handler, session string) *gateway.WorkerLocation {
	t.Helper()
	var events struct {
		Worker *gateway.WorkerLocation `json:"worker"`
	}
	polled := getWorkspace(handler, "/events?session="+url.QueryEscape(session), "")
	if err := json.Unmarshal(polled.Body.Bytes(), &events); err != nil {
		t.Fatalf("events = %d %s", polled.Code, polled.Body.String())
	}
	return events.Worker
}

// promptLater sends a prompt to a session, whose answer comes on the channel.
func promptLater(local http.Handler, session string) <-chan *httptest.ResponseRecorder {
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		answered <- postPrompt(local, socketHost, url.Values{"session": {session}, "message": {"Show the deterministic browser response"}})
	}()
	return answered
}

func waiting(answered <-chan *httptest.ResponseRecorder) bool {
	select {
	case <-answered:
		return false
	case <-time.After(300 * time.Millisecond):
		return true
	}
}

// A session moves alone: its move waits until the other sessions of its repository on this machine have finished
// their steps, holding their new prompts meanwhile, and stops only the session's own Pi, whose prompts wait until it
// runs on the worker.
func TestAMoveWaitsForTheRepositorysOtherSessionsAndHoldsTheirPrompts(t *testing.T) {
	handler, alpha, beta, project := fakePiGateway(t)
	local := localHandler(t, handler)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	for _, session := range []string{alpha, beta} {
		if response := postPrompt(local, socketHost, url.Values{"session": {session}, "message": {"Show the deterministic browser response"}}); response.Code != http.StatusOK {
			t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
		}
		waitForLocalSession(t, local, session, func(session gateway.LocalSession) bool { return session.State == "idle" })
	}
	if response := postPrompt(local, socketHost, url.Values{"session": {beta}, "message": {"Start the abort scenario"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
	waitForLocalSession(t, local, beta, func(session gateway.LocalSession) bool { return session.State == "working" })
	heldOthers := make(chan [2]<-chan *httptest.ResponseRecorder, 1)
	var heldOther, heldNew, heldSession <-chan *httptest.ResponseRecorder
	workers.snapshot = func() {
		held := <-heldOthers
		heldOther, heldNew = held[0], held[1]
		if !waiting(heldOther) || !waiting(heldNew) {
			t.Error("the other sessions' prompts didn't wait for the move's base")
		}
	}
	workers.copying = func() {
		for _, held := range []<-chan *httptest.ResponseRecorder{heldOther, heldNew} {
			if answer := <-held; answer.Code != http.StatusOK {
				t.Errorf("another session's prompt = %d %s", answer.Code, answer.Body.String())
			}
		}
		heldSession = promptLater(local, alpha)
		if !waiting(heldSession) {
			t.Error("the moving session's prompt didn't wait for the move")
		}
	}

	moved := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		moved <- command(local, http.MethodPost, "/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}})
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if location := workerOf(t, handler, alpha); location != nil && location.Waiting == "Prompt for beta-0002" && !location.Copying.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the moving session never said that it waits for the other: %+v", workerOf(t, handler, alpha))
		}
	}
	held := promptLater(local, beta)
	if !waiting(held) {
		t.Fatal("the other session's prompt didn't wait")
	}
	// A session that starts in the repository meanwhile waits too.
	created := serveAction(handler, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": project}, true))
	var fresh struct{ Session string }
	if err := json.Unmarshal(created.Body.Bytes(), &fresh); err != nil || created.Code != http.StatusOK {
		t.Fatalf("new session = %d %s", created.Code, created.Body.String())
	}
	heldFresh := promptLater(local, fresh.Session)
	if !waiting(heldFresh) {
		t.Fatal("the new session's prompt didn't wait")
	}
	heldOthers <- [2]<-chan *httptest.ResponseRecorder{held, heldFresh}
	command(local, http.MethodPost, "/abort", url.Values{"session": {beta}})
	if response := <-moved; response.Code != http.StatusOK {
		t.Fatalf("move = %d %s", response.Code, response.Body.String())
	}
	if answer := <-heldSession; answer.Code != http.StatusOK {
		t.Fatalf("the moved session's prompt = %d %s", answer.Code, answer.Body.String())
	}
	if _, stopped := fakePiPIDs(t, alpha); len(stopped) != 1 {
		t.Fatalf("the moved session's Pi stopped %v", stopped)
	}
	if _, stopped := fakePiPIDs(t, beta); len(stopped) != 0 {
		t.Fatalf("the other session's Pi stopped %v", stopped)
	}
	if location := workerOf(t, handler, alpha); location == nil || location.Waiting != "" || location.Worker != "beelink" {
		t.Fatalf("the moved session's location = %+v", location)
	}
}

func TestAMoveRefusesASessionThatWorksAndGivesUpWaitingForAnother(t *testing.T) {
	handler, alpha, beta, project := fakePiGateway(t)
	local := localHandler(t, handler)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	if response := postPrompt(local, socketHost, url.Values{"session": {beta}, "message": {"Start the abort scenario"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
	waitForLocalSession(t, local, beta, func(session gateway.LocalSession) bool { return session.State == "working" })

	if response := command(local, http.MethodPost, "/workers/move", url.Values{"session": {beta}, "worker": {"beelink"}}); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "working") {
		t.Fatalf("move of a working session = %d %s", response.Code, response.Body.String())
	}
	// A gripi command gives up with the command.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "http://gripi/workers/move", strings.NewReader(url.Values{"session": {alpha}, "worker": {"beelink"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response := httptest.NewRecorder()
	local.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Prompt for beta-0002") || !strings.Contains(response.Body.String(), "try again") {
		t.Fatalf("move while another session works = %d %s", response.Code, response.Body.String())
	}
	if len(workers.moves) != 0 {
		t.Fatalf("moves = %v", workers.moves)
	}
	// Nothing is held any more.
	command(local, http.MethodPost, "/abort", url.Values{"session": {beta}})
	waitForLocalSession(t, local, beta, func(session gateway.LocalSession) bool { return session.State == "idle" })
	for _, session := range []string{alpha, beta} {
		if response := postPrompt(local, socketHost, url.Values{"session": {session}, "message": {"Show the deterministic browser response"}}); response.Code != http.StatusOK {
			t.Fatalf("prompt after the refused move = %d %s", response.Code, response.Body.String())
		}
	}
	if started, stopped := fakePiPIDs(t, alpha); len(started) != 1 || len(stopped) != 0 {
		t.Fatalf("after the refused moves: Pi started %v, stopped %v", started, stopped)
	}
}

// A new session that Pi has not saved yet exists only in its Pi, which a move or a return stops, so it starts again
// afterwards under its key, with what was chosen in it, where it runs now. Meanwhile and afterwards, the page that
// shows it keeps showing it.
func TestANewSessionThatPiHasNotSavedStartsAgainWhereItMoved(t *testing.T) {
	cfg, _, _, project := fakePiConfig(t)
	workers := &fakeWorkers{repository: project}
	// Whether the session was on the worker at each start of its Pi, and whether the worker answers.
	var onWorker []bool
	answers := true
	var session string
	handler, err := gateway.NewPiHandlerWithLauncher(cfg, gripi.WebFiles, func(process agent.Process) (*exec.Cmd, func(), error) {
		placed := process.Key != "" && workers.onWorker(process.Key)
		if process.Dir == project {
			onWorker = append(onWorker, placed)
		}
		if placed && !answers {
			return nil, nil, errors.New("beelink is not answering")
		}
		return nil, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	created := serveAction(handler, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": project}, true))
	var payload struct{ Session string }
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil || created.Code != http.StatusOK {
		t.Fatalf("new session = %d %s", created.Code, created.Body.String())
	}
	session = payload.Session
	if response := postWorkspaceForm(handler, "/sessions/model_settings", url.Values{"session": {session}, "provider": {"e2e"}, "model": {"contract-model"}, "thinking": {"high"}}, ""); response.Code != http.StatusOK {
		t.Fatalf("model settings = %d %s", response.Code, response.Body.String())
	}
	// A shell command adds events, whose cursor a page keeps, and leaves the session unsaved.
	if response := serveAction(handler, formActionRequest("/prompt", map[string]string{"session": session, "message": "!printf hi"}, true)); response.Code != http.StatusOK {
		t.Fatalf("shell command = %d %s", response.Code, response.Body.String())
	}
	shown := func(when string) {
		t.Helper()
		if page := getWorkspace(handler, "/?session="+url.QueryEscape(session), ""); !strings.Contains(page.Body.String(), `data-events-url="/events?session=`+url.QueryEscape(session)+`"`) {
			t.Fatalf("%s, the page of the new session shows another: %d", when, page.Code)
		}
		// What a live page gets when it refreshes in the background, which goes to another session that it names.
		var fragment struct{ Session string }
		if response := getWorkspace(handler, "/session_fragment?session="+url.QueryEscape(session), ""); json.Unmarshal(response.Body.Bytes(), &fragment) != nil || fragment.Session != session {
			t.Fatalf("%s, the live page of the new session would show %q: %d", when, fragment.Session, response.Code)
		}
	}
	var cursor int64
	workers.copying = func() {
		shown("while the session moves")
		// The page's poll continues from its cursor and shows where the session goes.
		events := getWorkspace(handler, fmt.Sprintf("/events?session=%s&after=%d", url.QueryEscape(session), cursor), "")
		var payload struct {
			LastSeq int64                   `json:"last_seq"`
			Missed  bool                    `json:"missed"`
			Worker  *gateway.WorkerLocation `json:"worker"`
		}
		if json.Unmarshal(events.Body.Bytes(), &payload) != nil || payload.LastSeq != cursor || payload.Missed || payload.Worker == nil || payload.Worker.Copying.IsZero() {
			t.Fatalf("while the session moves, events = %d %s", events.Code, events.Body.String())
		}
	}

	for _, step := range []struct{ route, worker string }{{"/workers/move", "beelink"}, {"/workers/return", ""}} {
		cursor = fakePiEventCursor(t, handler, session)
		if response := postWorkspaceForm(handler, step.route, url.Values{"session": {session}, "worker": {step.worker}}, ""); response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", step.route, response.Code, response.Body.String())
		}
		shown("after " + step.route)
		if events := getWorkspace(handler, fmt.Sprintf("/events?session=%s&after=%d", url.QueryEscape(session), cursor), ""); events.Code != http.StatusOK || !strings.Contains(events.Body.String(), `"missed":false`) {
			t.Fatalf("after %s, events = %d %s", step.route, events.Code, events.Body.String())
		}
		if status := getWorkspace(handler, "/status?session="+url.QueryEscape(session), "").Body.String(); !strings.Contains(status, `"model":"e2e/contract-model"`) || !strings.Contains(status, `"thinking":"high"`) {
			t.Fatalf("after %s, the new session's status = %s", step.route, status)
		}
	}
	if started, stopped := fakePiPIDs(t, session); len(started) != 3 || len(stopped) != 2 || !slices.Equal(onWorker, []bool{false, true, false}) {
		t.Fatalf("Pi of the new session started %v, on the worker %v, and stopped %v", started, onWorker, stopped)
	}

	// A move whose copy fails leaves the session here, where it starts again.
	workers.copying = nil
	workers.copyFails = errors.New("rsync: connection unexpectedly closed")
	if response := postWorkspaceForm(handler, "/workers/move", url.Values{"session": {session}, "worker": {"beelink"}}, ""); response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "rsync") {
		t.Fatalf("move whose copy fails = %d %s", response.Code, response.Body.String())
	}
	if !slices.Equal(onWorker, []bool{false, true, false, false}) {
		t.Fatalf("after the failed move, Pi of the new session started on the worker %v", onWorker)
	}
	shown("after the failed move")
	workers.copyFails = nil

	// A session that cannot start again on the worker stays, which the move says, with the reason, and starts again
	// with what was chosen in it once it continues on main.
	answers = false
	response := postWorkspaceForm(handler, "/workers/move", url.Values{"session": {session}, "worker": {"beelink"}}, "")
	if response.Code == http.StatusOK || !strings.Contains(response.Body.String(), session) || !strings.Contains(response.Body.String(), "beelink is not answering") {
		t.Fatalf("move whose new session can't start again = %d %s", response.Code, response.Body.String())
	}
	if sidebar := getWorkspace(handler, "/sidebar", "").Body.String(); !strings.Contains(sidebar, url.QueryEscape(session)) {
		t.Fatalf("the sidebar lacks the new session that could not start again: %s", sidebar)
	}
	if response := postWorkspaceForm(handler, "/workers/return", url.Values{"session": {session}}, ""); response.Code != http.StatusOK {
		t.Fatalf("return = %d %s", response.Code, response.Body.String())
	}
	if status := getWorkspace(handler, "/status?session="+url.QueryEscape(session), "").Body.String(); !strings.Contains(status, `"model":"e2e/contract-model"`) || !strings.Contains(status, `"thinking":"high"`) {
		t.Fatalf("after the return, the new session's status = %s", status)
	}
}

// A worker's gripi command that prompts a session there while the session comes back waits for the return, and then
// reaches nothing: the session runs on main, which the worker's commands don't reach.
func TestAWorkersPromptThatAReturnHeldDoesNotRunTheSessionOnMain(t *testing.T) {
	handler, alpha, _, project := fakePiGateway(t)
	local := localHandler(t, handler)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	worker := workerHandler(handler, workers)
	if response := command(local, http.MethodPost, "/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}}); response.Code != http.StatusOK {
		t.Fatalf("move = %d %s", response.Code, response.Body.String())
	}
	var held <-chan *httptest.ResponseRecorder
	workers.snapshot = func() {
		held = promptLater(worker, alpha)
		if !waiting(held) {
			t.Error("the worker's prompt didn't wait for the return")
		}
	}

	if response := command(local, http.MethodPost, "/workers/return", url.Values{"session": {alpha}}); response.Code != http.StatusOK {
		t.Fatalf("return = %d %s", response.Code, response.Body.String())
	}
	if answer := <-held; answer.Code != http.StatusNotFound {
		t.Fatalf("the worker's held prompt = %d %s", answer.Code, answer.Body.String())
	}
	if started, _ := fakePiPIDs(t, alpha); len(started) != 0 {
		t.Fatalf("the worker's prompt started Pi on main: %v", started)
	}
}

// A new session that Pi had not saved when it moved exists only on its worker, so a new gateway learns of it from
// where it runs: the session is listed, continues on the worker and returns.
func TestANewSessionThatPiHasNotSavedStaysOnItsWorkerAfterARestart(t *testing.T) {
	cfg, _, _, project := fakePiConfig(t)
	workers := &fakeWorkers{repository: project}
	// Whether the session was on the worker at each start of its Pi.
	var onWorker []bool
	start := func() http.Handler {
		handler, err := gateway.NewPiHandlerWithLauncher(cfg, gripi.WebFiles, func(process agent.Process) (*exec.Cmd, func(), error) {
			if process.Dir == project {
				onWorker = append(onWorker, process.Key != "" && workers.onWorker(process.Key))
			}
			return nil, nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = handler.(interface{ Close(context.Context) error }).Close(context.Background()) })
		handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
		return handler
	}
	first := start()
	created := serveAction(first, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": project}, true))
	var payload struct{ Session string }
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil || created.Code != http.StatusOK {
		t.Fatalf("new session = %d %s", created.Code, created.Body.String())
	}
	session := payload.Session
	if response := postWorkspaceForm(first, "/workers/move", url.Values{"session": {session}, "worker": {"beelink"}}, ""); response.Code != http.StatusOK {
		t.Fatalf("move = %d %s", response.Code, response.Body.String())
	}
	if err := first.(interface{ Close(context.Context) error }).Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := start()
	local, worker := localHandler(t, second), workerHandler(second, workers)
	for _, commands := range []http.Handler{local, worker} {
		if listed := localSessions(t, commands, "?session="+url.QueryEscape(session)); len(listed) != 1 || listed[0].CWD != project {
			t.Fatalf("after a restart, the session is listed as %+v", listed)
		}
	}
	// A shell command leaves the session unsaved.
	if response := command(worker, http.MethodPost, "/prompt", url.Values{"session": {session}, "message": {"!printf hi"}}); response.Code != http.StatusOK {
		t.Fatalf("shell command on the worker = %d %s", response.Code, response.Body.String())
	}
	if response := command(local, http.MethodPost, "/workers/return", url.Values{"session": {session}}); response.Code != http.StatusOK {
		t.Fatalf("return = %d %s", response.Code, response.Body.String())
	}
	if response := postPrompt(local, socketHost, url.Values{"session": {session}, "message": {"Show the deterministic browser response"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt on main = %d %s", response.Code, response.Body.String())
	}
	if !slices.Equal(onWorker, []bool{false, true, true, false}) {
		t.Fatalf("Pi of the new session started on the worker %v", onWorker)
	}
}

// Main's copy of a session on a worker comes back from there, so deleting it would only lose its name, tags and
// attachments, and a session that it started in place would stay there.
func TestASessionOnAWorkerIsDeletedOrBranchedOnlyOnceItContinuesOnMain(t *testing.T) {
	handler, alpha, _, project := fakePiGateway(t)
	workers := &fakeWorkers{repository: project, placed: map[string]bool{alpha: true}}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	local := localHandler(t, handler)
	for name, post := range map[string]func() *httptest.ResponseRecorder{
		"browser": func() *httptest.ResponseRecorder {
			return postWorkspaceForm(handler, "/sessions/delete", url.Values{"session": {alpha}}, "")
		},
		"gripi": func() *httptest.ResponseRecorder {
			return command(local, http.MethodPost, "/sessions/delete", url.Values{"session": {alpha}})
		},
		"gripi on the worker": func() *httptest.ResponseRecorder {
			worker := workerHandler(handler, workerPlacement{sessions: []string{alpha}})
			return command(worker, http.MethodPost, "/sessions/delete", url.Values{"session": {alpha}})
		},
	} {
		if response := post(); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "beelink") || !strings.Contains(response.Body.String(), "continue on main") {
			t.Fatalf("delete from the %s = %d %s", name, response.Code, response.Body.String())
		}
	}
	if _, err := os.Stat(alpha); err != nil {
		t.Fatalf("the session's file: %v", err)
	}
	for name, response := range map[string]*httptest.ResponseRecorder{
		"/new":  command(local, http.MethodPost, "/prompt", url.Values{"session": {alpha}, "message": {"/new"}}),
		"clone": postWorkspaceForm(handler, "/sessions/clone", url.Values{"session": {alpha}}, ""),
	} {
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "continue on main") {
			t.Fatalf("%s on a worker = %d %s", name, response.Code, response.Body.String())
		}
	}

	workers.placed[alpha] = false
	if response := command(local, http.MethodPost, "/sessions/delete", url.Values{"session": {alpha}}); response.Code != http.StatusOK {
		t.Fatalf("delete on main = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(alpha); !os.IsNotExist(err) {
		t.Fatalf("deleted session file: %v", err)
	}
}

func TestWorkerCommandsTakeASessionAndRefuseWithAConflict(t *testing.T) {
	handler, alpha, beta, project := fakePiGateway(t)
	local := localHandler(t, handler)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)

	if response := command(local, http.MethodPost, "/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}}); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"session":"`+alpha+`"}` {
		t.Fatalf("move = %d %s", response.Code, response.Body.String())
	}
	if response := command(local, http.MethodPost, "/workers/forward", url.Values{"session": {alpha}, "port": {"5173"}}); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"local":5174,"port":5173}` {
		t.Fatalf("forward = %d %s", response.Code, response.Body.String())
	}
	for _, port := range []string{"", "0", "65536", "http"} {
		if response := command(local, http.MethodPost, "/workers/forward", url.Values{"session": {alpha}, "port": {port}}); response.Code != http.StatusBadRequest {
			t.Fatalf("forward of port %q = %d %s", port, response.Code, response.Body.String())
		}
	}
	if response := command(local, http.MethodPost, "/workers/unforward", url.Values{"session": {alpha}, "port": {"5173"}}); response.Code != http.StatusOK || len(workers.forwards) != 0 {
		t.Fatalf("unforward = %d %s; forwards %v", response.Code, response.Body.String(), workers.forwards)
	}
	for route, fields := range map[string]url.Values{
		"/workers/forward":   {"session": {beta}, "port": {"5173"}},
		"/workers/unforward": {"session": {alpha}, "port": {"5173"}},
		"/workers/move":      {"session": {alpha}, "worker": {"beelink"}},
		"/workers/return":    {"session": {beta}},
	} {
		if response := command(local, http.MethodPost, route, fields); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "not") && !strings.Contains(response.Body.String(), "already") {
			t.Fatalf("refused %s = %d %s", route, response.Code, response.Body.String())
		}
	}
	if response := command(local, http.MethodPost, "/workers/move", url.Values{"session": {beta}, "worker": {"nuc"}}); response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "Could not resolve hostname") {
		t.Fatalf("failed move = %d %s", response.Code, response.Body.String())
	}
	if response := command(local, http.MethodPost, "/workers/move", url.Values{"session": {filepath.Join(filepath.Dir(alpha), "missing.jsonl")}, "worker": {"beelink"}}); response.Code != http.StatusNotFound {
		t.Fatalf("move of an unknown session = %d %s", response.Code, response.Body.String())
	}
	if response := command(local, http.MethodPost, "/workers/return", url.Values{"session": {alpha}}); response.Code != http.StatusOK || !slices.Equal(workers.moves, []string{"to beelink", "back"}) {
		t.Fatalf("return = %d %s; moves %v", response.Code, response.Body.String(), workers.moves)
	}
	worker := workerHandler(handler, workerPlacement{sessions: []string{alpha}})
	if response := command(worker, http.MethodPost, "/workers/return", url.Values{"session": {alpha}}); response.Code != http.StatusForbidden {
		t.Fatalf("return from a worker = %d %s", response.Code, response.Body.String())
	}
}

// A session that the workers' launcher refuses to start, such as one that is moving, is a conflict, with the reason.
func TestASessionThatTheWorkersRefuseToStartIsAConflict(t *testing.T) {
	cfg := testConfig(t)
	cfg.PiCommand = []string{"false"}
	reason := "the session is moving between this machine and a worker; try again once it has moved"
	handler, err := gateway.NewPiHandlerWithLauncher(cfg, gripi.WebFiles, func(agent.Process) (*exec.Cmd, func(), error) {
		return nil, nil, refusal{errors.New(reason)}
	})
	if err != nil {
		t.Fatal(err)
	}

	response := serveAction(handler, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": t.TempDir()}, true))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), reason) {
		t.Fatalf("new session = %d %s", response.Code, response.Body.String())
	}
}

func TestTheBrowserMovesItsSessionAndSeesWhereItRuns(t *testing.T) {
	handler, alpha, beta, project := fakePiGateway(t)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	post := func(route string, fields url.Values) *httptest.ResponseRecorder {
		return postWorkspaceForm(handler, route, fields, "")
	}

	listed := getWorkspace(handler, "/workers?session="+url.QueryEscape(alpha), "")
	var choices struct {
		Workers    []gateway.Worker `json:"workers"`
		Repository struct {
			Main                string
			Roots               []string
			Worktrees, Sessions int
		} `json:"repository"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &choices); err != nil || listed.Code != http.StatusOK || len(choices.Workers) != 2 || choices.Workers[1].State != "unreachable" ||
		choices.Repository.Main != project || !slices.Equal(choices.Repository.Roots, []string{project, project + "-feature"}) || choices.Repository.Worktrees != 1 || choices.Repository.Sessions != 1 {
		t.Fatalf("workers = %d %s (%v)", listed.Code, listed.Body.String(), err)
	}
	if page := getWorkspace(handler, "/?session="+url.QueryEscape(alpha), ""); !strings.Contains(page.Body.String(), "data-workers-added") || strings.Contains(page.Body.String(), "session-worker-indicator") {
		t.Fatalf("page of a session on main = %s", page.Body.String())
	}

	if response := post("/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}}); response.Code != http.StatusOK || !slices.Equal(workers.moves, []string{"to beelink"}) {
		t.Fatalf("move = %d %s; moves %v", response.Code, response.Body.String(), workers.moves)
	}
	if response := post("/workers/forward", url.Values{"session": {alpha}, "port": {"5173"}}); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"local":5174`) {
		t.Fatalf("forward = %d %s", response.Code, response.Body.String())
	}
	if location := workerOf(t, handler, beta); location != nil {
		t.Fatalf("the other session of the repository runs on %+v", location)
	}
	workers.notices = []gateway.WorkerNotice{{Repository: project, Aside: "/state/returns/m1/worker", Paths: []string{project + "/README"}}}

	var events struct {
		Worker  *gateway.WorkerLocation `json:"worker"`
		Notices []gateway.WorkerNotice  `json:"worker_notices"`
	}
	polled := getWorkspace(handler, "/events?session="+url.QueryEscape(alpha), "")
	if err := json.Unmarshal(polled.Body.Bytes(), &events); err != nil || events.Worker == nil || events.Worker.Worker != "beelink" || events.Worker.State != "connected" ||
		!slices.Equal(events.Worker.Ports, []gateway.WorkerPort{{Port: 5173, Local: 5174}}) || len(events.Notices) != 1 || events.Notices[0].Aside != "/state/returns/m1/worker" {
		t.Fatalf("events = %d %s (%v)", polled.Code, polled.Body.String(), err)
	}
	// Only the moved session of the repository has the mark in the sidebar.
	page := getWorkspace(handler, "/?session="+url.QueryEscape(alpha), "").Body.String()
	if strings.Count(page, `class="session-worker-indicator" data-worker-state="connected" title="Runs on beelink · connected"`) != 1 || !strings.Contains(page, `data-worker="{&#34;worker&#34;:&#34;beelink&#34;`) || !strings.Contains(page, "/state/returns/m1/worker") {
		t.Fatalf("page of a session on a worker = %s", page)
	}

	if response := post("/workers/dismiss_notices", url.Values{"session": {alpha}}); response.Code != http.StatusOK || workers.notices != nil {
		t.Fatalf("dismiss = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/unforward", url.Values{"session": {alpha}, "port": {"5173"}}); response.Code != http.StatusOK {
		t.Fatalf("unforward = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/return", url.Values{"session": {alpha}}); response.Code != http.StatusOK || workers.onWorker(alpha) {
		t.Fatalf("return = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/move", url.Values{"session": {filepath.Join(filepath.Dir(alpha), "missing.jsonl")}, "worker": {"beelink"}}); response.Code != http.StatusNotFound {
		t.Fatalf("move of an unknown session = %d %s", response.Code, response.Body.String())
	}
}

func TestTheBrowserMovesOnlyTheUsersSessions(t *testing.T) {
	handler, alpha, beta, secret := multiUserFakePiGateway(t)
	project := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(alpha))))), "project")
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	cookie := "gripi_workspace=" + access.WorkspaceID(secret, alice)

	if response := postWorkspaceForm(handler, "/workers/move", url.Values{"session": {beta}, "worker": {"beelink"}}, cookie); response.Code != http.StatusNotFound {
		t.Fatalf("alice moving bob's session = %d %s", response.Code, response.Body.String())
	}
	if response := getWorkspace(handler, "/workers?session="+url.QueryEscape(beta), cookie); response.Code != http.StatusNotFound {
		t.Fatalf("alice listing workers for bob's session = %d %s", response.Code, response.Body.String())
	}
	if response := postWorkspaceForm(handler, "/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}}, cookie); response.Code != http.StatusOK || !slices.Equal(workers.moves, []string{"to beelink"}) {
		t.Fatalf("alice moving her session = %d %s; moves %v", response.Code, response.Body.String(), workers.moves)
	}
}
