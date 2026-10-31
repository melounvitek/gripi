package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// fakeWorkers moves the repository in one folder, and forwards ports of its worker, as far as the gateway sees it.
type fakeWorkers struct {
	repository string
	moves      []string
	forwards   map[int]int
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
	if err := stop(workers.inRepository); err != nil {
		return err
	}
	workers.moves = append(workers.moves, "to "+worker)
	return nil
}

func (workers *fakeWorkers) Return(_ context.Context, folder string, stop func(func(string) bool) error) error {
	if err := stop(workers.inRepository); err != nil {
		return err
	}
	workers.moves = append(workers.moves, "back")
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
