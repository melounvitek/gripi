//go:build !windows

package rpc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCloseTerminatesTheWholePiProcessGroup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required")
	}
	root := t.TempDir()
	pidPath := filepath.Join(root, "child.pid")
	scriptPath := filepath.Join(root, "process.mjs")
	script := `import { spawn } from "node:child_process"; import { writeFileSync } from "node:fs";
const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" });
writeFileSync(process.env.CHILD_PID_PATH, String(child.pid));
process.stdin.resume(); setInterval(() => {}, 1000);`
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHILD_PID_PATH", pidPath)
	client, err := Start(filepath.Join(root, "unused.jsonl"), []string{node, scriptPath}, scriptPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		contents, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			childPID, _ = strconv.Atoi(strings.TrimSpace(string(contents)))
			if childPID > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if childPID == 0 {
		_ = client.Close()
		t.Fatal("child process did not start")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("descendant process %d survived client close", childPID)
}
