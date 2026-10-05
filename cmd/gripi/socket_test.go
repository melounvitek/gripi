package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// Socket paths have a short system limit, which t.TempDir can exceed on macOS.
func socketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "gripi-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	return filepath.Join(directory, "state", "gripi.sock")
}

func socketClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
}

func TestLocalServerListensOnAPrivateSocketAndReplacesAStaleOne(t *testing.T) {
	path := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	server, err := startLocalServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		io.WriteString(response, "local")
	}), path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode = %s", info.Mode())
	}
	response, err := socketClient(path).Get("http://gripi/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if body, _ := io.ReadAll(response.Body); string(body) != "local" {
		t.Fatalf("body = %q", body)
	}
}

func TestLocalServerLeavesARunningGatewayAndOrdinaryFilesAlone(t *testing.T) {
	path := socketPath(t)
	running, err := startLocalServer(http.NotFoundHandler(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	if _, err := startLocalServer(http.NotFoundHandler(), path); err == nil {
		t.Fatal("second gateway took over a live socket")
	}
	if response, err := socketClient(path).Get("http://gripi/"); err != nil {
		t.Fatalf("first gateway stopped answering: %v", err)
	} else {
		response.Body.Close()
	}

	// A socket that cannot be probed may belong to a live gateway.
	unreadable := filepath.Join(filepath.Dir(path), "unreadable.sock")
	stale, err := net.Listen("unix", unreadable)
	if err != nil {
		t.Fatal(err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	if err := os.Chmod(unreadable, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := startLocalServer(http.NotFoundHandler(), unreadable); err == nil && os.Getuid() != 0 {
		t.Fatal("gateway replaced a socket it could not probe")
	}

	file := filepath.Join(filepath.Dir(path), "notes.txt")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := startLocalServer(http.NotFoundHandler(), file); err == nil {
		t.Fatal("gateway listened on an ordinary file's path")
	}
	if contents, err := os.ReadFile(file); err != nil || string(contents) != "keep" {
		t.Fatalf("ordinary file = %q, %v", contents, err)
	}
}
