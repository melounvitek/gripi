package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

// cannedGateway answers the session list on a private socket that the commands will find.
func cannedGateway(t *testing.T, sessions []gateway.LocalSession) {
	t.Helper()
	path := socketPath(t)
	server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/sessions" {
			http.NotFound(response, request)
			return
		}
		json.NewEncoder(response).Encode(map[string]any{"sessions": sessions})
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
			CWD: fmt.Sprintf("/work/project-%02d", index), State: "idle", Tags: []string{}, UpdatedAt: time.Date(2026, 1, 1, 0, 0, index, 0, time.UTC),
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

func TestSessionCommandsExplainAnUnreachableGateway(t *testing.T) {
	t.Setenv("GRIPI_SOCKET_PATH", socketPath(t))
	code, stdout, stderr := runCLI("list")
	// Suggesting 'gripi serve' here would have agents start a second gateway.
	if code != 1 || stdout != "" || !strings.Contains(stderr, "cannot reach the gateway") || strings.Contains(stderr, "gripi serve") {
		t.Fatalf("gripi list without a gateway = %d, stdout %q, stderr %q", code, stdout, stderr)
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
