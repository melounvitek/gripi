package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
	fakePi(t, `read -r request
printf '%s\n' '{"id":"check","type":"response","command":"get_commands","success":true,"data":{"commands":[`+fakePiCommands+`]}}'
`)

	if code, stdout, stderr := runCLI("check"); code != 0 || stderr != "" {
		t.Fatalf("gripi check = %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gripi check ran hermes: %v", err)
	}
}
