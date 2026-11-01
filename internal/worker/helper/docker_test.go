package helper_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// fakeDocker serves Docker's /_ping on a Unix socket, and records the bodies of container creations.
func fakeDocker(t *testing.T) (socket string, created <-chan string) {
	t.Helper()
	socket = filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	bodies := make(chan string, 10)
	daemon := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			fmt.Fprint(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			body, _ := io.ReadAll(r.Body)
			bodies <- string(body)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"Id":"c1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	daemon.Listener = listener
	daemon.Start()
	t.Cleanup(daemon.Close)
	return socket, bodies
}

func TestSessionsInTheViewReachDockerThroughTheHelper(t *testing.T) {
	upstream, created := fakeDocker(t)
	home := os.Getenv("GRIPI_TEST_VIEW_HOME")
	own, _ := os.UserHomeDir()
	flags := []string{"--home", home}
	if home == "" {
		// Where the view can't be entered, serve runs as it runs itself in the view.
		home, own = "/nonexistent-gripi-home/vitek", t.TempDir()
		flags = []string{"--home", home, "--own-home", own}
	}
	unit := startUnitHelperWith(t, helper.DefaultLease, []string{"DOCKER_HOST=unix://" + upstream}, flags...)

	host := unit.environment("s1", "DOCKER_HOST=tcp://main:2375")["DOCKER_HOST"]
	socket, ok := strings.CutPrefix(host, "unix://")
	if !ok {
		t.Fatalf("Pi got DOCKER_HOST=%s", host)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}}
	response, err := client.Get("http://docker/_ping")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "OK" {
		t.Fatalf("/_ping = %s %q", response.Status, body)
	}
	response, err = client.Post("http://docker/v1.52/containers/create", "application/json", strings.NewReader(`{"HostConfig":{"Binds":["`+home+`/proj:/x"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("/containers/create = %s", response.Status)
	}
	if got, want := strings.TrimSpace(<-created), `{"HostConfig":{"Binds":["`+own+`/proj:/x"]}}`; got != want {
		t.Errorf("Docker got %s, want %s", got, want)
	}
}

func TestSessionsOutsideTheViewKeepTheirDockerHost(t *testing.T) {
	home, _ := os.UserHomeDir()
	unit := startUnitHelperWith(t, helper.DefaultLease, []string{"DOCKER_HOST=unix:///run/workers-docker.sock"}, "--home", home)

	if host := unit.environment("s1", "DOCKER_HOST=tcp://main:2375")["DOCKER_HOST"]; host != "tcp://main:2375" {
		t.Errorf("with main's DOCKER_HOST, Pi got %q", host)
	}
	if host := unit.environment("s2")["DOCKER_HOST"]; host != "unix:///run/workers-docker.sock" {
		t.Errorf("without main's DOCKER_HOST, Pi got %q", host)
	}
}

// TestRealDockerInTheView runs docker in a session of a helper that serves as it does in the view, against this
// machine's Docker, with main's home folder at a path that doesn't exist here, so the container finds the file only if
// the helper's Docker socket moves the path to the worker user's home folder.
func TestRealDockerInTheView(t *testing.T) {
	if os.Getenv("GRIPI_TEST_DOCKER") == "" {
		t.Skip("set GRIPI_TEST_DOCKER=1 to run docker in a session against this machine's Docker")
	}
	if output, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker doesn't work: %v\n%s", err, output)
	}
	if exec.Command("docker", "image", "inspect", "alpine").Run() != nil {
		if output, err := exec.Command("docker", "pull", "alpine").CombinedOutput(); err != nil {
			t.Skipf("can't pull alpine: %v\n%s", err, output)
		}
		t.Cleanup(func() { exec.Command("docker", "image", "rm", "alpine").Run() })
	}
	const home = "/nonexistent-gripi-home/vitek"
	if _, err := os.Stat(home); err == nil {
		t.Fatalf("%s exists, so the test can't tell whether the helper moves paths", home)
	}
	own := t.TempDir()
	if err := os.Mkdir(filepath.Join(own, "proj"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, "proj", "file"), []byte("hello from the worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	unit := startUnitHelperWith(t, helper.DefaultLease, nil, "--home", home, "--own-home", own)

	// --mount fails for a missing folder, where -v would make the daemon create it at main's path on this machine.
	script := "read -r line && docker run --rm --mount type=bind,source=" + home + "/proj,target=/x alpine cat /x/file 2>&1"
	if response := unit.request(map[string]any{"type": "session.create", "session": "docker", "cwd": own, "args": []string{"/bin/sh", "-c", script}}); !response.OK {
		t.Fatalf("session.create = %+v", response)
	}
	_, input, output := unit.attach("docker")
	fmt.Fprintln(input, "go")
	if got, _ := io.ReadAll(output); string(got) != "hello from the worker\n" {
		t.Fatalf("docker run in the session: %q", got)
	}
}
