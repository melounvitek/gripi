package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/agent/pi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// answeringPi is a configured Pi that passes gripi check.
const answeringPi = `read -r request
printf '%s\n' '{"id":"check","type":"response","command":"get_commands","success":true,"data":{"commands":[` + fakePiCommands + `]}}'
`

// installUnusedHermes puts a hermes on PATH that leaves a file behind when anything runs it, and returns that file.
func installUnusedHermes(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	marker := filepath.Join(directory, "ran")
	if err := os.WriteFile(filepath.Join(directory, "hermes"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

// fakeHermes makes the browser suite's fake Hermes the gateway's Hermes and returns the file it logs to.
func fakeHermes(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	log := filepath.Join(t.TempDir(), "hermes.log")
	t.Setenv("GRIPI_HERMES", filepath.Join(filepath.Dir(file), "..", "..", "e2e", "support", "fake_hermes.mjs"))
	t.Setenv("FAKE_HERMES_LOG", log)
	return log
}

// fakeHermesSession is a session saved in the fake Hermes's state, as session/list reports it.
type fakeHermesSession struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title"`
	UpdatedAt string `json:"updatedAt"`
}

// hermesGateway runs fakePiGateway's gateway with the fake Hermes enabled, which has saved the given sessions.
func hermesGateway(t *testing.T, sessions ...fakeHermesSession) (alpha, beta string) {
	t.Helper()
	fakeHermes(t)
	state := filepath.Join(t.TempDir(), "hermes-state.json")
	encoded, err := json.Marshal(map[string]any{"sessions": sessions})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERMES_STATE", state)
	return fakePiGateway(t, func(cfg *config.Config) { cfg.HermesPath = os.Getenv("GRIPI_HERMES") })
}

func listedSessions(t *testing.T) []gateway.LocalSession {
	t.Helper()
	code, stdout, stderr := runCLI("list", "--all", "--json")
	if code != 0 {
		t.Fatalf("gripi list = %d, stderr %q", code, stderr)
	}
	var sessions []gateway.LocalSession
	if err := json.Unmarshal([]byte(stdout), &sessions); err != nil {
		t.Fatal(err)
	}
	return sessions
}

// configuredHermes makes a shell script the gateway's Hermes.
func configuredHermes(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hermes")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GRIPI_HERMES", path)
}

func TestTheGatewayNeverRunsHermesWithoutGripiHermes(t *testing.T) {
	marker := installUnusedHermes(t)
	fakePiGateway(t)

	if code, _, stderr := runCLI("send", "0a1-a", "Show the deterministic browser response"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	if code, _, stderr := runCLI("wait", "0a1-a"); code != 0 {
		t.Fatalf("gripi wait = %d, stderr %q", code, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the gateway ran hermes: %v", err)
	}
}

func TestCheckNeverRunsHermesWithoutGripiHermes(t *testing.T) {
	marker := installUnusedHermes(t)
	fakePi(t, answeringPi)

	if code, stdout, stderr := runCLI("check"); code != 0 || stderr != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gripi check ran hermes: %v", err)
	}
}

func TestCheckStartsTheConfiguredHermesWithoutTheGatewaysSettings(t *testing.T) {
	fakePi(t, answeringPi)
	log := fakeHermes(t)
	// A gateway started from a Hermes session would otherwise file Gripi's sessions under that session's platform.
	t.Setenv("HERMES_SESSION_SOURCE", "telegram")

	if code, stdout, stderr := runCLI("check"); code != 0 || stderr != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	var records []map[string]any
	file, err := os.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for scanner := bufio.NewScanner(file); scanner.Scan(); {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	if len(records) < 2 || records[1]["method"] != "initialize" {
		t.Fatalf("Hermes was not asked to initialize: %v", records)
	}
	start := records[0]
	if argv, _ := json.Marshal(start["argv"]); string(argv) != `["acp"]` || start["sessionSource"] != nil {
		t.Fatalf("Hermes started as %v", start)
	}
	settings, _ := json.Marshal(start["gripiSettings"])
	if strings.Contains(string(settings), "GRIPI_ADMIN_PASSWORD") || strings.Contains(string(settings), "GRIPI_HERMES") {
		t.Fatalf("Hermes got the gateway's settings %s", settings)
	}
}

func TestCheckFailsWithHermesOwnError(t *testing.T) {
	fakePi(t, answeringPi)
	configuredHermes(t, `echo "ModuleNotFoundError: No module named 'acp'" >&2
exit 1
`)

	code, stdout, stderr := runCLI("check")
	if code != 1 || stdout != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, expected := range []string{"gripi check: ", "Hermes exited with status 1", "No module named 'acp'"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("stderr %q does not contain %q", stderr, expected)
		}
	}
}

func TestCheckFailsWhenHermesCannotListAndLoadItsSessions(t *testing.T) {
	fakePi(t, answeringPi)
	fakeHermes(t)
	t.Setenv("FAKE_HERMES_CAPABILITIES", `{"loadSession":true,"sessionCapabilities":{}}`)

	code, _, stderr := runCLI("check")
	if code != 1 || !strings.Contains(stderr, "Hermes cannot list and load its sessions") {
		t.Fatalf("gripi check = %d, stderr %q", code, stderr)
	}
}

func TestCheckStillChecksPiWhenHermesIsConfigured(t *testing.T) {
	fakePi(t, "exit 3\n")
	fakeHermes(t)

	if code, _, stderr := runCLI("check"); code != 1 || !strings.Contains(stderr, "Pi exited with status 3") {
		t.Fatalf("gripi check = %d, stderr %q", code, stderr)
	}
}

func TestTheGatewayGetsPiAloneWithoutGripiHermes(t *testing.T) {
	adapter, err := newAdapter(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(*pi.Adapter); !ok {
		t.Fatalf("adapter = %T", adapter)
	}

	adapter, err = newAdapter(config.Config{HermesPath: "/opt/hermes"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.(*agent.Router); !ok {
		t.Fatalf("adapter with Hermes = %T", adapter)
	}
}

func TestHermesSessionsAreListedWithPisSessions(t *testing.T) {
	project := t.TempDir()
	var saved []fakeHermesSession
	// More than one page of Hermes's list, all more recent than the Pi sessions.
	for index := range 60 {
		saved = append(saved, fakeHermesSession{SessionID: fmt.Sprintf("hermes-%02d", index), CWD: project, Title: fmt.Sprintf("Hermes session %02d", index), UpdatedAt: fmt.Sprintf("2026-03-01T10:%02d:00Z", index)})
	}
	alpha, _ := hermesGateway(t, saved...)

	sessions := listedSessions(t)
	if len(sessions) != 62 {
		t.Fatalf("listed %d sessions", len(sessions))
	}
	newest := sessions[0]
	if newest.Path != "hermes:hermes-59" || newest.ID != "hermes-59" || newest.Name != "Hermes session 59" || newest.CWD != project || newest.State != "idle" {
		t.Fatalf("newest session = %+v", newest)
	}
	if !newest.UpdatedAt.Equal(time.Date(2026, 3, 1, 10, 59, 0, 0, time.UTC)) {
		t.Fatalf("newest session updated at %s", newest.UpdatedAt.Time)
	}
	if sessions[59].Path != "hermes:hermes-00" || sessions[60].Path != filepath.Join(filepath.Dir(alpha), "beta.jsonl") || sessions[61].Path != alpha {
		t.Fatalf("sessions are not ordered by activity: %+v", sessions[58:])
	}

	// Pi sessions still run with Pi.
	if code, _, stderr := runCLI("send", "0a1-a", "Show the deterministic browser response"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	if code, stdout, stderr := runCLI("wait", "0a1-a", "--json"); code != 0 || decodeSession(t, stdout).LastReply != "Deterministic browser response complete." {
		t.Fatalf("gripi wait = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestGripiListNamesTheAgentOnlyWithHermes(t *testing.T) {
	fakePiGateway(t)
	if _, stdout, _ := runCLI("list", "--json"); strings.Contains(stdout, `"agent"`) {
		t.Fatalf("gripi list without Hermes names agents:\n%s", stdout)
	}
	if _, stdout, _ := runCLI("list"); strings.Contains(stdout, "AGENT") {
		t.Fatalf("gripi list without Hermes has an agent column:\n%s", stdout)
	}
}

func TestGripiListsSendsToAndShowsHermesSessions(t *testing.T) {
	hermesGateway(t, fakeHermesSession{SessionID: "hermes-e2e", CWD: t.TempDir(), Title: "A Hermes session", UpdatedAt: "2026-03-01T10:00:00Z"})

	sessions := listedSessions(t)
	if sessions[0].ID != "hermes-e2e" || sessions[0].Agent != "hermes" || sessions[1].Agent != "pi" {
		t.Fatalf("listed sessions = %+v", sessions)
	}
	if _, stdout, _ := runCLI("list"); !strings.HasPrefix(stdout, "ID") || !strings.Contains(stdout, "AGENT") || !strings.Contains(stdout, "hermes") {
		t.Fatalf("gripi list = %q", stdout)
	}
	if code, _, stderr := runCLI("send", "hermes-e2e", "List the files"); code != 0 {
		t.Fatalf("gripi send = %d, stderr %q", code, stderr)
	}
	if code, stdout, stderr := runCLI("wait", "hermes-e2e", "--json"); code != 0 || decodeSession(t, stdout).LastReply != "Hermes says: List the files" {
		t.Fatalf("gripi wait = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if code, stdout, stderr := runCLI("show", "hermes-e2e"); code != 0 || !strings.Contains(stdout, "Hermes says: List the files") {
		t.Fatalf("gripi show = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Hermes's CLI would write the session next to the gateway's Hermes, with nothing to keep them apart.
	if code, _, stderr := runCLI("open", "hermes-e2e"); code == 0 || !strings.Contains(stderr, "Pi sessions only") {
		t.Fatalf("gripi open = %d, stderr %q", code, stderr)
	}
}
