package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
