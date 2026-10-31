package helper_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// git runs Git in dir, with no configuration of the user's.
func git(t *testing.T, dir string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

// extensionRequest sends one request to a session's extension socket, as the worker extension does, and returns the
// answer.
func extensionRequest(t *testing.T, runtimeDir, session, kind string) helper.Response {
	t.Helper()
	connection, err := net.Dial("unix", filepath.Join(runtimeDir, session+".sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	fmt.Fprintf(connection, `{"id":1,"type":%q}`+"\n", kind)
	connection.SetReadDeadline(time.Now().Add(20 * time.Second))
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var answer helper.Response
	if err := json.Unmarshal(line, &answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func TestMirrorSendsTheUnitToMainAndAnswersTheBarriersOfItsSessions(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	root := t.TempDir()
	project := filepath.Join(root, "worker", "project")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	git(t, project, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(project, "README.md"), []byte("first\n"), 0644)
	git(t, project, "add", "-A")
	git(t, project, "commit", "-qm", "first")
	repository, err := mirror.Discover(project)
	if err != nil {
		t.Fatal(err)
	}
	pids := filepath.Join(root, "pids")
	if response := unit.request(map[string]any{
		"type": "session.create", "session": "s1", "cwd": filepath.Join(project), "args": []string{unit.pi, "--mode", "rpc"}, "env": []string{"FAKE_PI_PIDS=" + pids},
	}); !response.OK {
		t.Fatalf("session.create = %+v", response)
	}
	if answer := extensionRequest(t, unit.runtimeDir, "s1", "barrier"); answer.OK {
		t.Fatal("a barrier passed before the unit was mirrored")
	}

	if response := unit.request(map[string]any{"type": "mirror.start", "unit": repository}); !response.OK {
		t.Fatalf("mirror.start = %+v", response)
	}
	stranger := exec.Command(buildGripi(t), "worker", "mirror", "--runtime-dir", unit.runtimeDir, project, "0123456789abcdef")
	if output, err := stranger.CombinedOutput(); err == nil || !strings.Contains(string(output), "not 0123456789abcdef") {
		t.Fatalf("mirror for another helper = %v, %s", err, output)
	}
	mainCopy := filepath.Join(root, "main")
	destination, err := mirror.NewMovedDestination(filepath.Join(root, "state"), filepath.Join(root, "worker"), mainCopy)
	if err != nil {
		t.Fatal(err)
	}
	channel := exec.Command(buildGripi(t), "worker", "mirror", "--runtime-dir", unit.runtimeDir, project, unit.instance)
	channel.Stderr = os.Stderr
	input, _ := channel.StdinPipe()
	output, _ := channel.StdoutPipe()
	if err := channel.Start(); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		served <- destination.Serve(context.Background(), struct {
			io.Reader
			io.Writer
		}{output, input}, filepath.Join(root, "aside"))
		input.Close()
	}()

	os.WriteFile(filepath.Join(project, "tool.txt"), []byte("written by a tool\n"), 0644)
	git(t, project, "add", "-A")
	git(t, project, "commit", "-qm", "by a tool")
	if answer := extensionRequest(t, unit.runtimeDir, "s1", "barrier"); !answer.OK {
		t.Fatalf("barrier = %+v", answer)
	}
	if contents, _ := os.ReadFile(filepath.Join(mainCopy, "project", "tool.txt")); string(contents) != "written by a tool\n" {
		t.Fatalf("main's copy of tool.txt = %q", contents)
	}
	if head := git(t, filepath.Join(mainCopy, "project"), "rev-parse", "HEAD"); head != git(t, project, "rev-parse", "HEAD") {
		t.Fatalf("main's copy is at %s", head)
	}
	if answer := extensionRequest(t, unit.runtimeDir, "s1", "health"); !answer.OK {
		t.Fatalf("health = %+v", answer)
	}

	os.WriteFile(filepath.Join(project, "last.txt"), []byte("just before the move back\n"), 0644)
	if response := unit.request(map[string]any{"type": "mirror.stop", "path": project}); !response.OK {
		t.Fatalf("mirror.stop = %+v", response)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("the destination stopped with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the mirror still runs")
	}
	if err := channel.Wait(); err != nil {
		t.Fatalf("gripi worker mirror: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainCopy, "project", "last.txt")); err != nil {
		t.Fatalf("the change before mirror.stop: %v", err)
	}
	if answer := extensionRequest(t, unit.runtimeDir, "s1", "barrier"); answer.OK {
		t.Fatal("a barrier passed after the mirror stopped")
	}
}

func TestASessionsMirrorMakesItsFoldersAndSendsChangesUnasked(t *testing.T) {
	unit := startUnitHelper(t, helper.DefaultLease)
	root := t.TempDir()
	project, feature := filepath.Join(root, "worker", "sessions", "--project--"), filepath.Join(root, "worker", "sessions", "--project-feature--")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	sessions := mirror.Unit{Main: project, Worktrees: []string{feature}}
	if response := unit.request(map[string]any{"type": "mirror.start", "unit": sessions, "sessions": true}); !response.OK {
		t.Fatalf("mirror.start = %+v", response)
	}
	if info, err := os.Stat(feature); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("the folder of the sessions in the feature: %v, %v", info, err)
	}
	mainCopy := filepath.Join(root, "main")
	destination, err := mirror.NewMovedDestination(filepath.Join(root, "state"), filepath.Join(root, "worker"), mainCopy)
	if err != nil {
		t.Fatal(err)
	}
	channel := exec.Command(buildGripi(t), "worker", "mirror", "--runtime-dir", unit.runtimeDir, project, unit.instance)
	channel.Stderr = os.Stderr
	input, _ := channel.StdinPipe()
	output, _ := channel.StdoutPipe()
	if err := channel.Start(); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		served <- destination.Serve(context.Background(), struct {
			io.Reader
			io.Writer
		}{output, input}, filepath.Join(root, "aside"))
		input.Close()
	}()

	os.WriteFile(filepath.Join(feature, "session.jsonl"), []byte("{\"type\":\"session\"}\n"), 0600)
	copied := filepath.Join(mainCopy, "sessions", "--project-feature--", "session.jsonl")
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if contents, _ := os.ReadFile(copied); string(contents) == "{\"type\":\"session\"}\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("main never got the new session")
		}
	}
	if response := unit.request(map[string]any{"type": "mirror.stop", "path": project}); !response.OK {
		t.Fatalf("mirror.stop = %+v", response)
	}
	if err := <-served; err != nil {
		t.Fatalf("the destination stopped with %v", err)
	}
	channel.Wait()
}
