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

// The helper's view shows what was next to main's home folder when the helper started. Copies see that view, so a
// folder made there later can't be copied.
func TestCopiesSeeTheFoldersOfTheHelpersView(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "alice")
	if err := os.MkdirAll(filepath.Dir(home), 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(buildGripi(t), "worker", "enter", "--home", home, "--", "true").CombinedOutput(); err != nil {
		t.Skipf("this system has no private views: %v %s", err, output)
	}
	unit := startUnitHelperWith(t, helper.DefaultLease, nil, "--home", home)
	project := filepath.Join(root, "home", "projects", "app")
	mainCopy := t.TempDir()
	for _, folder := range []string{project, filepath.Join(mainCopy, "app")} {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "README"), []byte("main's project\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	channel := exec.Command(buildGripi(t), "worker", "receive", "--runtime-dir", unit.runtimeDir, filepath.Join(mainCopy, "app"), project, unit.instance)
	var stderr strings.Builder
	channel.Stderr = &stderr
	input, _ := channel.StdinPipe()
	output, _ := channel.StdoutPipe()
	if err := channel.Start(); err != nil {
		t.Fatal(err)
	}
	synced := mirror.Sync(context.Background(), struct {
		io.Reader
		io.WriteCloser
	}{output, input}, mirror.Unit{Main: filepath.Join(mainCopy, "app")}, mirror.Options{})
	if err := channel.Wait(); synced == nil || err == nil || !strings.Contains(stderr.String(), "read-only file system") {
		t.Fatalf("a copy into a folder that the helper's view lacks = %v, %v %s", synced, err, stderr.String())
	}
}
