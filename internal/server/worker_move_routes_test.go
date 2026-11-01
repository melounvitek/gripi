package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gripi "github.com/melounvitek/gripi"
	"github.com/melounvitek/gripi/internal/access"
	"github.com/melounvitek/gripi/internal/agent"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// fakeWorkers moves the repository in one folder, and forwards ports of its worker, as far as the gateway sees it.
type fakeWorkers struct {
	repository string
	moves      []string
	forwards   map[int]int
	placed     bool
	notices    []gateway.WorkerNotice
}

// refusal is an error of Workers that refuses what was asked.
type refusal struct{ error }

func (refusal) Refused() bool { return true }

func (workers *fakeWorkers) inRepository(folder string) bool {
	return folder == workers.repository || strings.HasPrefix(folder, workers.repository+"/")
}

func (workers *fakeWorkers) Move(_ context.Context, folder, worker string, stop func(func(string) bool) error) error {
	if !workers.inRepository(folder) {
		return refusal{errors.New(folder + " is not a repository")}
	}
	if worker != "beelink" {
		return errors.New("ssh: Could not resolve hostname " + worker)
	}
	if workers.placed {
		return refusal{errors.New(folder + " is on worker beelink already")}
	}
	if err := stop(workers.inRepository); err != nil {
		return err
	}
	workers.moves = append(workers.moves, "to "+worker)
	workers.placed = true
	return nil
}

func (workers *fakeWorkers) Return(_ context.Context, folder string, stop func(func(string) bool) error) error {
	if err := stop(workers.inRepository); err != nil {
		return err
	}
	workers.moves = append(workers.moves, "back")
	workers.placed = false
	return nil
}

func (workers *fakeWorkers) Forward(_ context.Context, folder string, port int) (int, error) {
	if !workers.inRepository(folder) {
		return 0, refusal{errors.New(folder + " is not on a worker")}
	}
	if workers.forwards == nil {
		workers.forwards = map[int]int{}
	}
	workers.forwards[port] = port + 1
	return port + 1, nil
}

func (workers *fakeWorkers) Unforward(folder string, port int) error {
	if _, found := workers.forwards[port]; !found || !workers.inRepository(folder) {
		return refusal{errors.New("the port is not forwarded")}
	}
	delete(workers.forwards, port)
	return nil
}

func (workers *fakeWorkers) List() ([]gateway.Worker, error) {
	return []gateway.Worker{{Name: "beelink", Destination: "gripi@beelink", State: "connected"}, {Name: "nuc", Destination: "gripi@nuc", State: "unreachable"}}, nil
}

func (workers *fakeWorkers) Location(folder string) (gateway.WorkerLocation, bool) {
	if !workers.placed || !workers.inRepository(folder) {
		return gateway.WorkerLocation{}, false
	}
	location := gateway.WorkerLocation{Worker: "beelink", State: "connected", Repository: workers.repository, Ports: []gateway.WorkerPort{}}
	for port, local := range workers.forwards {
		location.Ports = append(location.Ports, gateway.WorkerPort{Port: port, Local: local})
	}
	return location, true
}

func (workers *fakeWorkers) Repository(folder string) (string, []string, error) {
	return workers.repository, []string{workers.repository + "-feature"}, nil
}

func (workers *fakeWorkers) Notices(folder string) []gateway.WorkerNotice {
	if !workers.inRepository(folder) {
		return nil
	}
	return workers.notices
}

func (workers *fakeWorkers) DismissNotices(folder string) {
	if workers.inRepository(folder) {
		workers.notices = nil
	}
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
		case record.Event == "stopped" && contains(pids, record.PID):
			stopped = append(stopped, record.PID)
		}
	}
	return started, stopped
}

func contains(values []float64, value float64) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func TestMovingARepositoryStopsItsSessionsUnlessOneIsWorking(t *testing.T) {
	handler, alpha, _, project := fakePiGateway(t)
	local := localHandler(t, handler)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)
	if response := postPrompt(local, socketHost, url.Values{"session": {alpha}, "message": {"Start the abort scenario"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
	waitForLocalSession(t, local, alpha, func(session gateway.LocalSession) bool { return session.State == "working" })

	if response := command(local, http.MethodPost, "/workers/move", url.Values{"folder": {project}, "worker": {"beelink"}}); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "working") {
		t.Fatalf("move while a session works = %d %s", response.Code, response.Body.String())
	}
	if started, stopped := fakePiPIDs(t, alpha); len(started) != 1 || len(stopped) != 0 || len(workers.moves) != 0 {
		t.Fatalf("after the refused move: Pi started %v, stopped %v; moves %v", started, stopped, workers.moves)
	}

	command(local, http.MethodPost, "/abort", url.Values{"session": {alpha}})
	waitForLocalSession(t, local, alpha, func(session gateway.LocalSession) bool { return session.State == "idle" })
	if response := command(local, http.MethodPost, "/workers/move", url.Values{"folder": {project}, "worker": {"beelink"}}); response.Code != http.StatusOK {
		t.Fatalf("move = %d %s", response.Code, response.Body.String())
	}
	if started, stopped := fakePiPIDs(t, alpha); len(started) != 1 || len(stopped) != 1 {
		t.Fatalf("after the move: Pi started %v, stopped %v", started, stopped)
	}
	if response := command(local, http.MethodPost, "/workers/return", url.Values{"folder": {project}}); response.Code != http.StatusOK {
		t.Fatalf("return = %d %s", response.Code, response.Body.String())
	}
	if len(workers.moves) != 2 {
		t.Fatalf("moves = %v", workers.moves)
	}

	worker := workerHandler(handler, workerPlacement{folders: []string{project}})
	if response := command(worker, http.MethodPost, "/workers/return", url.Values{"folder": {project}}); response.Code != http.StatusForbidden {
		t.Fatalf("return from a worker = %d %s", response.Code, response.Body.String())
	}
}

// The workers stop the Pi of the sessions on this machine that Pi would keep in a session folder that a repository on a
// worker takes, such as a new session that Pi has not saved yet, whose file the worker's copy of the folder would
// replace, and refuse while one of them works.
func TestTheWorkersStopTheSessionsOfAFolderUnlessOneIsWorking(t *testing.T) {
	handler, _, _, project := fakePiGateway(t)
	local := localHandler(t, handler)
	newSession := func(cwd string) string {
		t.Helper()
		response := serveAction(handler, formActionRequest("/sessions/new_at_cwd", map[string]string{"cwd": cwd}, true))
		var created struct{ Session string }
		if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || response.Code != http.StatusOK {
			t.Fatalf("new session = %d %s", response.Code, response.Body.String())
		}
		return created.Session
	}
	idleFolder, workingFolder := t.TempDir(), t.TempDir()
	idle, working := newSession(idleFolder), newSession(workingFolder)
	if response := postPrompt(local, socketHost, url.Values{"session": {working}, "message": {"Start the abort scenario"}}); response.Code != http.StatusOK {
		t.Fatalf("prompt = %d %s", response.Code, response.Body.String())
	}
	waitForLocalSession(t, local, working, func(session gateway.LocalSession) bool { return session.State == "working" })
	stop := handler.(interface{ StopSessions(func(string) bool) error }).StopSessions

	err := stop(func(folder string) bool { return folder == idleFolder || folder == workingFolder })
	var refusal interface{ Refused() bool }
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), working) {
		t.Fatalf("stop while a session works = %v", err)
	}
	for _, session := range []string{idle, working} {
		if started, stopped := fakePiPIDs(t, session); len(started) != 1 || len(stopped) != 0 {
			t.Fatalf("after the refused stop: Pi of %s started %v, stopped %v", session, started, stopped)
		}
	}

	if err := stop(func(folder string) bool { return folder == idleFolder || folder == project }); err != nil {
		t.Fatal(err)
	}
	if started, stopped := fakePiPIDs(t, idle); len(started) != 1 || len(stopped) != 1 {
		t.Fatalf("after the stop: Pi of the idle session started %v, stopped %v", started, stopped)
	}
	if _, stopped := fakePiPIDs(t, working); len(stopped) != 0 {
		t.Fatalf("the stop ended the Pi of a session in another folder: %v", stopped)
	}
}

// Main's copy of a session on a worker comes back from there, so deleting it would only lose its name, tags and
// attachments.
func TestASessionOnAWorkerIsDeletedOnlyOnceItContinuesOnMain(t *testing.T) {
	handler, alpha, _, project := fakePiGateway(t)
	workers := &fakeWorkers{repository: project, placed: true}
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
			worker := workerHandler(handler, workerPlacement{sessions: []string{alpha}, folders: []string{project}})
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

	workers.placed = false
	if response := command(local, http.MethodPost, "/sessions/delete", url.Values{"session": {alpha}}); response.Code != http.StatusOK {
		t.Fatalf("delete on main = %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(alpha); !os.IsNotExist(err) {
		t.Fatalf("deleted session file: %v", err)
	}
}

func TestWorkerCommandsForwardPortsAndRefuseWithAConflict(t *testing.T) {
	handler, _, _, project := fakePiGateway(t)
	local := localHandler(t, handler)
	workers := &fakeWorkers{repository: project}
	handler.(interface{ SetWorkers(gateway.Workers) }).SetWorkers(workers)

	if response := command(local, http.MethodPost, "/workers/forward", url.Values{"folder": {project + "/web"}, "port": {"5173"}}); response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"local":5174,"port":5173}` {
		t.Fatalf("forward = %d %s", response.Code, response.Body.String())
	}
	for _, port := range []string{"", "0", "65536", "http"} {
		if response := command(local, http.MethodPost, "/workers/forward", url.Values{"folder": {project}, "port": {port}}); response.Code != http.StatusBadRequest {
			t.Fatalf("forward of port %q = %d %s", port, response.Code, response.Body.String())
		}
	}
	if response := command(local, http.MethodPost, "/workers/unforward", url.Values{"folder": {project}, "port": {"5173"}}); response.Code != http.StatusOK || len(workers.forwards) != 0 {
		t.Fatalf("unforward = %d %s; forwards %v", response.Code, response.Body.String(), workers.forwards)
	}
	for route, fields := range map[string]url.Values{
		"/workers/forward":   {"folder": {"/elsewhere"}, "port": {"5173"}},
		"/workers/unforward": {"folder": {project}, "port": {"5173"}},
		"/workers/move":      {"folder": {"/elsewhere"}, "worker": {"beelink"}},
	} {
		if response := command(local, http.MethodPost, route, fields); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "not") {
			t.Fatalf("refused %s = %d %s", route, response.Code, response.Body.String())
		}
	}
	if response := command(local, http.MethodPost, "/workers/move", url.Values{"folder": {project}, "worker": {"nuc"}}); response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "Could not resolve hostname") {
		t.Fatalf("failed move = %d %s", response.Code, response.Body.String())
	}
}

// A session that the workers' launcher refuses to start, such as one that Pi would keep in a session folder of a
// repository on a worker, is a conflict, with the reason.
func TestASessionThatTheWorkersRefuseToStartIsAConflict(t *testing.T) {
	cfg := testConfig(t)
	cfg.PiCommand = []string{"false"}
	reason := "/work/a/b and /work/a-b, which is on worker beelink, would share Pi's session folder"
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

func TestTheBrowserMovesTheRepositoryOfItsSessionAndSeesWhereItRuns(t *testing.T) {
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
			Worktrees, Sessions int
		} `json:"repository"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &choices); err != nil || listed.Code != http.StatusOK || len(choices.Workers) != 2 || choices.Workers[1].State != "unreachable" ||
		choices.Repository.Main != project || choices.Repository.Worktrees != 1 || choices.Repository.Sessions != 2 {
		t.Fatalf("workers = %d %s (%v)", listed.Code, listed.Body.String(), err)
	}
	if page := getWorkspace(handler, "/?session="+url.QueryEscape(alpha), ""); !strings.Contains(page.Body.String(), "data-workers-added") || strings.Contains(page.Body.String(), "session-worker-indicator") {
		t.Fatalf("page of a session on main = %s", page.Body.String())
	}

	if response := post("/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}}); response.Code != http.StatusOK || !slices.Equal(workers.moves, []string{"to beelink"}) {
		t.Fatalf("move = %d %s; moves %v", response.Code, response.Body.String(), workers.moves)
	}
	if response := post("/workers/move", url.Values{"session": {beta}, "worker": {"beelink"}}); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "already") {
		t.Fatalf("move of a repository on a worker = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/forward", url.Values{"session": {beta}, "port": {"5173"}}); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"local":5174`) {
		t.Fatalf("forward = %d %s", response.Code, response.Body.String())
	}
	workers.notices = []gateway.WorkerNotice{{Repository: project, Aside: "/state/aside", Paths: []string{project + "/README"}}}

	var events struct {
		Worker  *gateway.WorkerLocation `json:"worker"`
		Notices []gateway.WorkerNotice  `json:"worker_notices"`
	}
	polled := getWorkspace(handler, "/events?session="+url.QueryEscape(alpha), "")
	if err := json.Unmarshal(polled.Body.Bytes(), &events); err != nil || events.Worker == nil || events.Worker.Worker != "beelink" || events.Worker.State != "connected" ||
		!slices.Equal(events.Worker.Ports, []gateway.WorkerPort{{Port: 5173, Local: 5174}}) || len(events.Notices) != 1 || events.Notices[0].Aside != "/state/aside" {
		t.Fatalf("events = %d %s (%v)", polled.Code, polled.Body.String(), err)
	}
	page := getWorkspace(handler, "/?session="+url.QueryEscape(alpha), "").Body.String()
	if strings.Count(page, `class="session-worker-indicator" data-worker-state="connected" title="Runs on beelink · connected"`) != 2 || !strings.Contains(page, `data-worker="{&#34;worker&#34;:&#34;beelink&#34;`) || !strings.Contains(page, "/state/aside") {
		t.Fatalf("page of a session on a worker = %s", page)
	}

	if response := post("/workers/dismiss_notices", url.Values{"session": {alpha}}); response.Code != http.StatusOK || workers.notices != nil {
		t.Fatalf("dismiss = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/unforward", url.Values{"session": {alpha}, "port": {"5173"}}); response.Code != http.StatusOK {
		t.Fatalf("unforward = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/return", url.Values{"session": {alpha}}); response.Code != http.StatusOK || workers.placed {
		t.Fatalf("return = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/move", url.Values{"session": {filepath.Join(filepath.Dir(alpha), "missing.jsonl")}, "worker": {"beelink"}}); response.Code != http.StatusNotFound {
		t.Fatalf("move of an unknown session = %d %s", response.Code, response.Body.String())
	}
	if response := post("/workers/move", url.Values{"folder": {project}, "worker": {"beelink"}}); response.Code != http.StatusNotFound {
		t.Fatalf("move of a folder from the browser = %d %s", response.Code, response.Body.String())
	}
}

func TestTheBrowserMovesOnlyRepositoriesWhoseSessionsAreAllTheUsers(t *testing.T) {
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
	if response := postWorkspaceForm(handler, "/workers/move", url.Values{"session": {alpha}, "worker": {"beelink"}}, cookie); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "other users") || len(workers.moves) != 0 {
		t.Fatalf("alice moving a repository with bob's session = %d %s; moves %v", response.Code, response.Body.String(), workers.moves)
	}
}
