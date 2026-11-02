package helper_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
