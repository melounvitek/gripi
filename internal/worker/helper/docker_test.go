package helper_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
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

// Main learns on connecting whom the sessions run as and whether they can use Docker, and if not, what to do about it.
func TestTheHelloSaysWhomSessionsRunAsAndWhetherTheyCanUseDocker(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may use any socket")
	}
	socket, _ := fakeDocker(t)
	locked := filepath.Join(t.TempDir(), "locked.sock")
	listener, err := net.Listen("unix", locked)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ upstream, docker string }{
		{"unix://" + socket, "available"},
		{"unix://" + locked, "sessions on worker beelink run as " + me.Username + ", who can't use Docker there (dial unix " + locked + ": connect: permission denied). " +
			"An admin of beelink can add " + me.Username + " to the docker group or set up rootless Docker for it, then restart " + me.Username +
			"'s processes so they get the group: sudo systemctl restart user@$(id -u " + me.Username + ").service"},
		{"unix:///nonexistent/docker.sock", "sessions on worker beelink run as " + me.Username + ", who can't use Docker there (dial unix /nonexistent/docker.sock: connect: no such file or directory). " +
			"An admin of beelink can install or start Docker there, or set up rootless Docker for " + me.Username},
	} {
		unit := startUnitHelperWith(t, helper.DefaultLease, []string{"DOCKER_HOST=" + test.upstream}, "--name", "beelink")
		if unit.hello.User != me.Username || unit.hello.Docker != test.docker {
			t.Errorf("with Docker at %s, the hello says user %q, Docker %q; want %q", test.upstream, unit.hello.User, unit.hello.Docker, test.docker)
		}
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
