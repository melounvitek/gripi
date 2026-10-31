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

// fakeWorkers moves the repository in one folder, as far as the gateway sees it.
type fakeWorkers struct {
	repository string
	moves      []string
}

func (workers *fakeWorkers) inRepository(folder string) bool {
	return folder == workers.repository || strings.HasPrefix(folder, workers.repository+"/")
}

func (workers *fakeWorkers) Move(_ context.Context, folder, worker string, stop func(func(string) bool) error) error {
	if !workers.inRepository(folder) {
		return errors.New("not a repository")
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
