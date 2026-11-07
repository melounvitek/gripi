package sessionfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiCLINamesASessionByItsPathOrID(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	path := filepath.Join(directory, "sessions", "session.jsonl")
	for arguments, names := range map[string]bool{
		"--session " + path:                true,
		"--session=" + path:                true,
		"--session sessions/session.jsonl": true,
		"--session 0a1b2c3d":               true,
		"--session 0a1b":                   true,
		"--session-id 0a1b2c3d-1111":       true,
		"--session-id=0a1b2c3d-1111":       true,
		"--session-id 0a1b":                false,
		"--session 0a1c":                   false,
		"--session " + path + ".other":     false,
		"--fork " + path:                   false,
		"--name 0a1b2c3d":                  false,
		"--session":                        false,
		"--session other --name " + path:   false,
		"-p --session-id 0a1b2c3d-1111 hi": true,
	} {
		if namesSession(append([]string{"node", "pi"}, strings.Fields(arguments)...), os.Getpid(), path, "0a1b2c3d-1111") != names {
			t.Errorf("pi %s names the session: %v", arguments, !names)
		}
	}
}

func TestTheGatewaysOwnProcessesDoNotCount(t *testing.T) {
	if !descendsFrom(os.Getpid(), os.Getppid()) || descendsFrom(os.Getppid(), os.Getpid()) {
		t.Fatal("descendsFrom confuses the test process with its parent")
	}
}
