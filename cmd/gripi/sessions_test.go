package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// cannedGateway answers the session list on a private socket that the commands will find.
// It takes every prompt the way Pi takes an extension command: without starting a turn.
func cannedGateway(t *testing.T, sessions []gateway.LocalSession) {
	t.Helper()
	path := socketPath(t)
	server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /sessions":
			json.NewEncoder(response).Encode(map[string]any{"sessions": sessions})
		case "POST /prompt":
			json.NewEncoder(response).Encode(map[string]any{"session": request.FormValue("session"), "disposition": "handled"})
		default:
			http.NotFound(response, request)
		}
	}), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	t.Setenv("GRIPI_SOCKET_PATH", path)
}

func cannedSessions(count int) []gateway.LocalSession {
	sessions := make([]gateway.LocalSession, count)
	for index := range sessions {
		sessions[index] = gateway.LocalSession{
			ID: fmt.Sprintf("session-%02d", index), Path: fmt.Sprintf("/sessions/%02d.jsonl", index), Name: fmt.Sprintf("Task %02d", index),
			CWD: fmt.Sprintf("/work/project-%02d", index), State: "idle", Tags: []string{}, UpdatedAt: gateway.Timestamp{Time: time.Date(2026, 1, 1, 0, 0, index, 0, time.UTC)},
		}
	}
	sessions[0].State, sessions[0].Unread, sessions[0].Name = "working", true, "Fix the\nflaky   test"
	sessions[0].Pinned, sessions[0].Tags = true, []string{"cli", "urgent"}
	return sessions
}

func TestListPrintsTheLatestSessionsAsATable(t *testing.T) {
	cannedGateway(t, cannedSessions(25))
	code, stdout, stderr := runCLI("list")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if code != 0 || len(lines) != 21 {
		t.Fatalf("gripi list = %d with %d lines, stderr %q:\n%s", code, len(lines), stderr, stdout)
	}
	if header := strings.Fields(lines[0]); !reflect.DeepEqual(header, []string{"ID", "STATE", "UNREAD", "PINNED", "UPDATED", "PROJECT", "TAGS", "NAME"}) {
		t.Fatalf("header = %q", lines[0])
	}
	first, second := strings.Fields(lines[1]), strings.Fields(lines[2])
	if strings.Join(append(first[:4:4], first[5:]...), " ") != "session-00 working yes yes project-00 cli,urgent Fix the flaky test" {
		t.Fatalf("busy, unread, pinned and tagged row = %q", lines[1])
	}
	if strings.Join(append(second[:4:4], second[5:]...), " ") != "session-01 idle - - project-01 - Task 01" {
		t.Fatalf("idle row = %q", lines[2])
	}
	if !strings.Contains(stderr, "20 of 25") || !strings.Contains(stderr, "--all") {
		t.Fatalf("truncation is not explained: %q", stderr)
	}

	code, stdout, stderr = runCLI("list", "--all")
	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); code != 0 || len(lines) != 26 || stderr != "" {
		t.Fatalf("gripi list --all = %d with %d lines, stderr %q", code, len(lines), stderr)
	}
}

func TestListPrintsJSONForPrograms(t *testing.T) {
	sessions := cannedSessions(25)
	cannedGateway(t, sessions)
	code, stdout, stderr := runCLI("list", "--json")
	var listed []gateway.LocalSession
	// Nothing but JSON is printed, so "2>&1 | jq" keeps working.
	if err := json.Unmarshal([]byte(stdout), &listed); err != nil || code != 0 || stderr != "" {
		t.Fatalf("gripi list --json = %d, stderr %q, %v:\n%s", code, stderr, err, stdout)
	}
	if !reflect.DeepEqual(listed, sessions[:20]) {
		t.Fatalf("listed %+v, expected %+v", listed, sessions[:20])
	}
}

func TestJSONOutputHasOneTimestampFormatThatSortsAsText(t *testing.T) {
	path := socketPath(t)
	// The shapes that one listing used to mix: the source's zone, and only as many digits as it had.
	server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/conversation" {
			io.WriteString(response, `{"messages":[{"role":"user","text":"Hello","timestamp":"2026-10-06T20:47:02.72565778+02:00"}]}`)
			return
		}
		io.WriteString(response, `{"sessions":[
			{"id":"zoned","path":"/sessions/zoned.jsonl","updated_at":"2026-10-06T20:47:02.72565778+02:00"},
			{"id":"short","path":"/sessions/short.jsonl","updated_at":"2026-10-06T18:54:46.11Z"},
			{"id":"whole","path":"/sessions/whole.jsonl","updated_at":"2026-07-20T12:36:04Z"}]}`)
	}), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	t.Setenv("GRIPI_SOCKET_PATH", path)

	_, listed, _ := runCLI("list", "--json")
	for _, expected := range []string{`"updated_at": "2026-10-06T18:47:02.725Z"`, `"updated_at": "2026-10-06T18:54:46.110Z"`, `"updated_at": "2026-07-20T12:36:04.000Z"`} {
		if !strings.Contains(listed, expected) {
			t.Fatalf("gripi list --json has no %s:\n%s", expected, listed)
		}
	}
	if _, shown, _ := runCLI("show", "zoned", "--all", "--json"); !strings.Contains(shown, `"timestamp": "2026-10-06T18:47:02.725Z"`) {
		t.Fatalf("gripi show --all --json = %s", shown)
	}
}

func TestSendDoesNotWaitForATurnThatPiWillNotStart(t *testing.T) {
	cannedGateway(t, cannedSessions(2))
	started := time.Now()
	code, stdout, stderr := runCLI("send", "session-01", "/extension-command", "--json")
	if session := decodeSession(t, stdout); code != 0 || stderr != "" || session.ID != "session-01" || session.State != "idle" {
		t.Fatalf("gripi send = %d, stderr %q, session %+v", code, stderr, session)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("gripi send waited %s for a turn to start", elapsed)
	}
}

func TestSendRetriesOnlyARefusalThatWillPassInAMoment(t *testing.T) {
	defer func(window time.Duration) { retryWindow = window }(retryWindow)
	retryWindow = 500 * time.Millisecond
	pending := `{"code":"session_operation_pending","error":"Another session operation is pending. Please retry."}`
	for name, test := range map[string]struct {
		status           int
		retryAfter, body string
		// refusals is how many prompts the gateway refuses before it accepts one.
		refusals, code int32
	}{
		"another operation is pending":  {status: http.StatusConflict, body: pending, refusals: 2},
		"Pi is restarting":              {status: http.StatusServiceUnavailable, retryAfter: "1", body: `{"error":"Pi RPC client is restarting"}`, refusals: 2},
		"an operation stays pending":    {status: http.StatusConflict, body: pending, refusals: 1000, code: 1},
		"the session tree is changing":  {status: http.StatusConflict, body: `{"code":"session_operation_pending","retryable":false,"error":"The session tree is changing."}`, refusals: 1, code: 1},
		"Pi did not answer the gateway": {status: http.StatusGatewayTimeout, body: `{"error":"Pi RPC command timed out: prompt"}`, refusals: 1, code: 1},
	} {
		t.Run(name, func(t *testing.T) {
			path := socketPath(t)
			var prompts atomic.Int32
			server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch {
				case request.URL.Path == "/sessions":
					json.NewEncoder(response).Encode(map[string]any{"sessions": cannedSessions(2)})
				case prompts.Add(1) > test.refusals:
					json.NewEncoder(response).Encode(map[string]any{"session": request.FormValue("session"), "disposition": "handled"})
				default:
					if test.retryAfter != "" {
						response.Header().Set("Retry-After", test.retryAfter)
					}
					response.WriteHeader(test.status)
					io.WriteString(response, test.body)
				}
			}), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { server.Close() })
			t.Setenv("GRIPI_SOCKET_PATH", path)

			code, stdout, stderr := runCLI("send", "session-01", "message")
			if int32(code) != test.code {
				t.Fatalf("gripi send = %d, stdout %q, stderr %q", code, stdout, stderr)
			}
			if code == 0 {
				// The refusals are not the caller's business once the message is delivered.
				if stderr != "" || !strings.Contains(stdout, "session-01") || prompts.Load() != test.refusals+1 {
					t.Fatalf("delivered after %d prompts, stdout %q, stderr %q", prompts.Load(), stdout, stderr)
				}
				return
			}
			if stdout != "" || !strings.Contains(stderr, fmt.Sprintf("the gateway answered %d: ", test.status)) {
				t.Fatalf("refused send printed stdout %q, stderr %q", stdout, stderr)
			}
			if retried := prompts.Load() > 1; retried != (test.refusals > 1) {
				t.Fatalf("the gateway got %d prompts", prompts.Load())
			}
		})
	}
}

func TestNewNamesARelativeDirectoryInFullBecauseTheGatewayRunsElsewhere(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, requested := socketPath(t), ""
	server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/sessions/new_at_cwd" {
			requested = request.FormValue("cwd")
		}
		json.NewEncoder(response).Encode(map[string]any{"session": "/sessions/00.jsonl", "sessions": cannedSessions(1)})
	}), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	t.Setenv("GRIPI_SOCKET_PATH", path)
	t.Chdir(directory)
	if code, _, stderr := runCLI("new", "."); code != 0 || requested != directory {
		t.Fatalf("gripi new . = %d, stderr %q, asked for %q instead of %q", code, stderr, requested, directory)
	}
}

func TestSessionCommandsExplainAnUnreachableGateway(t *testing.T) {
	t.Setenv("GRIPI_SOCKET_PATH", socketPath(t))
	code, stdout, stderr := runCLI("list")
	// Suggesting 'gripi serve' here would have agents start a second gateway.
	if code != 1 || stdout != "" || !strings.Contains(stderr, "cannot reach the gateway") || strings.Contains(stderr, "gripi serve") {
		t.Fatalf("gripi list without a gateway = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// stallingGateway answers the session listing with one working session the given number of
// times. After that it accepts requests without answering them, as a paused gateway does.
func stallingGateway(t *testing.T, answers int32) {
	t.Helper()
	path, stop := socketPath(t), make(chan struct{})
	var remaining atomic.Int32
	remaining.Store(answers)
	server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if remaining.Add(-1) < 0 {
			<-stop
			return
		}
		json.NewEncoder(response).Encode(map[string]any{"sessions": cannedSessions(1)})
	}), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(stop)
		server.Close()
	})
	t.Setenv("GRIPI_SOCKET_PATH", path)
}

func TestSessionCommandsGiveUpOnAGatewayThatDoesNotAnswer(t *testing.T) {
	stallingGateway(t, 0)
	defer func(read, action time.Duration) { readTimeout, actionTimeout = read, action }(readTimeout, actionTimeout)
	readTimeout, actionTimeout = 200*time.Millisecond, 200*time.Millisecond
	for _, arguments := range [][]string{{"list"}, {"new", t.TempDir()}} {
		started := time.Now()
		code, stdout, stderr := runCLI(arguments...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "did not answer") {
			t.Fatalf("gripi %s = %d, stdout %q, stderr %q", arguments[0], code, stdout, stderr)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("gripi %s gave up only after %s", arguments[0], elapsed)
		}
	}
}

func TestWaitKeepsItsTimeoutWhenTheGatewayStopsAnswering(t *testing.T) {
	// The gateway stops before the command's first request, and once the wait has begun.
	for _, answers := range []int32{0, 1} {
		stallingGateway(t, answers)
		started := time.Now()
		code, stdout, stderr := runCLI("wait", "session-00", "--timeout", "0.5", "--json")
		// Exit 3 would claim that the session is known to be still working.
		if code != 1 || stdout != "" || !strings.Contains(stderr, "did not answer") {
			t.Fatalf("gripi wait after %d answers = %d, stdout %q, stderr %q", answers, code, stdout, stderr)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Fatalf("gripi wait --timeout 0.5 after %d answers took %s", answers, elapsed)
		}
	}
}

func TestListRejectsUnknownArguments(t *testing.T) {
	cannedGateway(t, cannedSessions(1))
	for _, arguments := range [][]string{{"list", "extra"}, {"list", "--bogus"}} {
		code, stdout, stderr := runCLI(arguments...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help list") {
			t.Fatalf("gripi %v = %d, stdout %q, stderr %q", arguments, code, stdout, stderr)
		}
	}
}
