package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakePiCommands = `{"name":"gripi_reload"},{"name":"gripi_tree_navigate"},{"name":"gripi_tree_snapshot"},{"name":"gripi_tree_leaf"},{"name":"gripi_tree_label"},{"name":"gripi_scoped_models"}`

// isolateSettings hides the settings of a gateway that runs these tests, as an update from an older version does.
func isolateSettings(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		if key, _, _ := strings.Cut(entry, "="); strings.HasPrefix(key, "GRIPI_") {
			t.Setenv(key, "")
			os.Unsetenv(key)
		}
	}
	t.Setenv("GRIPI_ENV_PATH", filepath.Join(t.TempDir(), "env"))
	t.Setenv("GRIPI_ADMIN_PASSWORD", "secret")
}

// fakePi makes the gateway's configured Pi a shell script and returns the file the script may log to.
func fakePi(t *testing.T, script string) string {
	t.Helper()
	isolateSettings(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "pi")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(directory, "log")
	t.Setenv("GRIPI_NODE", "/bin/sh")
	t.Setenv("GRIPI_PI", path)
	t.Setenv("FAKE_PI_LOG", log)
	return log
}

func TestCheckPassesWithTheInstalledPi(t *testing.T) {
	isolateSettings(t)

	if code, stdout, stderr := runCLI("check"); code != 0 || stderr != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestCheckRunsTheConfiguredPiWithAThrowawayPiConfig(t *testing.T) {
	userConfig := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", userConfig)
	log := fakePi(t, `read -r request
for extension; do :; done
test -s "$extension" || exit 9
printf '%s\n' "$PI_CODING_AGENT_DIR" > "$FAKE_PI_LOG"
printf '%s\n' '{"type":"extension_ui_request","id":"other"}' '{"id":"check","type":"response","command":"get_commands","success":true,"data":{"commands":[`+fakePiCommands+`]}}'
`)

	if code, stdout, stderr := runCLI("check"); code != 0 || stderr != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	contents, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	piConfig := strings.TrimSpace(string(contents))
	if piConfig == "" || piConfig == userConfig {
		t.Fatalf("Pi ran with the user's config directory %q", piConfig)
	}
	if _, err := os.Stat(piConfig); !os.IsNotExist(err) {
		t.Fatalf("throwaway Pi config %s was left behind: %v", piConfig, err)
	}
}

func TestCheckFailsWithPisOwnError(t *testing.T) {
	fakePi(t, `echo 'Error: Failed to load extension: pi.registerCommand is not a function' >&2
exit 1
`)

	code, stdout, stderr := runCLI("check")
	if code != 1 || stdout != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	for _, expected := range []string{"gripi check: ", "exited with status 1", "pi.registerCommand is not a function"} {
		if !strings.Contains(stderr, expected) {
			t.Fatalf("stderr %q does not contain %q", stderr, expected)
		}
	}
}

func TestCheckFailsWithTheErrorPiAnswers(t *testing.T) {
	fakePi(t, `read -r request
printf '%s\n' '{"id":"check","type":"response","command":"get_commands","success":false,"error":"Unknown command: get_commands"}'
`)

	if code, _, stderr := runCLI("check"); code != 1 || !strings.Contains(stderr, "Unknown command: get_commands") {
		t.Fatalf("gripi check = %d, stderr %q", code, stderr)
	}
}

func TestCheckNamesTheCommandsPiDidNotList(t *testing.T) {
	fakePi(t, `read -r request
printf '%s\n' '{"id":"check","type":"response","command":"get_commands","success":true,"data":{"commands":[{"name":"gripi_reload"},{"name":"gripi_tree_leaf"}]}}'
`)

	code, _, stderr := runCLI("check")
	if code != 1 || !strings.Contains(stderr, "gripi_tree_snapshot") || strings.Contains(stderr, "gripi_reload") {
		t.Fatalf("gripi check = %d, stderr %q", code, stderr)
	}
}
