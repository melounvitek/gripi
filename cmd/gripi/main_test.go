package main

import (
	"bytes"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// startupLog runs the gateway the test's environment configures on a free port
// with a temporary home, stops it once it listens, and returns what it logged.
func startupLog(t *testing.T) string {
	t.Helper()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	free.Close()
	t.Setenv("GRIPI_PORT", strconv.Itoa(free.Addr().(*net.TCPAddr).Port))
	t.Setenv("GRIPI_SOCKET_PATH", socketPath(t))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PI_CODING_AGENT_DIR", "")
	path := filepath.Join(t.TempDir(), "log")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	log.SetOutput(file)
	defer log.SetOutput(os.Stderr)

	stopped := make(chan error, 1)
	go func() { stopped <- serve() }()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		logged, _ := os.ReadFile(path)
		// serve handles signals by the time it logs this line.
		if strings.Contains(string(logged), "Gripi listening on") {
			break
		}
		select {
		case err := <-stopped:
			t.Fatalf("gateway stopped before listening: %v\n%s", err, logged)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway never listened:\n%s", logged)
		}
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	logged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(logged)
}

func TestServeWarnsAtStartWhenTheConfiguredPiCannotBeFound(t *testing.T) {
	// The gateway finds its checkout with git at start, so PATH keeps git.
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	withoutPi, withPi := t.TempDir(), t.TempDir()
	for _, directory := range []string{withoutPi, withPi} {
		if err := os.Symlink(git, filepath.Join(directory, "git")); err != nil {
			t.Fatal(err)
		}
	}
	pi, missing := filepath.Join(withPi, "pi"), filepath.Join(withoutPi, "missing")
	if err := os.WriteFile(pi, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		environment map[string]string
		lookedFor   string
	}{
		"pi is not on PATH":             {map[string]string{"PATH": withoutPi}, `"pi": executable file not found in $PATH`},
		"pi is on PATH":                 {map[string]string{"PATH": withPi}, ""},
		"GRIPI_NODE is missing":         {map[string]string{"GRIPI_NODE": missing, "GRIPI_PI": pi}, missing},
		"GRIPI_PI is missing":           {map[string]string{"GRIPI_NODE": "/bin/sh", "GRIPI_PI": missing}, missing},
		"GRIPI_NODE and GRIPI_PI exist": {map[string]string{"GRIPI_NODE": "/bin/sh", "GRIPI_PI": pi}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			isolateSettings(t)
			for key, value := range test.environment {
				t.Setenv(key, value)
			}

			logged := startupLog(t)
			warned := strings.Contains(logged, "Sessions cannot start until Pi is installed (https://pi.dev/)")
			if test.lookedFor == "" && warned {
				t.Fatalf("start-up log warns although Pi is there:\n%s", logged)
			}
			if test.lookedFor != "" && (!warned || !strings.Contains(logged, test.lookedFor)) {
				t.Fatalf("start-up log does not warn naming %q:\n%s", test.lookedFor, logged)
			}
		})
	}
}

func TestServeLogsTheURLToOpenOnALoopbackBind(t *testing.T) {
	isolateSettings(t)

	logged := startupLog(t)
	if expected := "Gripi listening on http://localhost:" + os.Getenv("GRIPI_PORT") + "\n"; !strings.Contains(logged, expected) {
		t.Fatalf("start-up log does not contain %q:\n%s", expected, logged)
	}
}

func TestStartupAddressIsAURLOnlyWhereLocalhostReachesTheGateway(t *testing.T) {
	for address, expected := range map[string]string{
		"localhost:8080":  "http://localhost:8080",
		"[::1]:4567":      "http://localhost:4567",
		"100.64.0.1:4567": "100.64.0.1:4567",
		"0.0.0.0:4567":    "0.0.0.0:4567",
		"[::]:4567":       "[::]:4567",
	} {
		if actual := startupAddress(address); actual != expected {
			t.Errorf("startupAddress(%q) = %q, expected %q", address, actual, expected)
		}
	}
}

func TestHTTPServerBoundsReadsAndIdleConnectionsWithoutBoundingResponses(t *testing.T) {
	server := newHTTPServer(nil)
	if server.ReadTimeout != 10*time.Minute || server.IdleTimeout != 2*time.Minute || server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("timeouts = read %s, idle %s, header %s", server.ReadTimeout, server.IdleTimeout, server.ReadHeaderTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("write timeout = %s; long RPC responses must remain unbounded", server.WriteTimeout)
	}
}

func TestEnsurePasswordAppendsOnceAndPreservesExistingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "env")
	original := []byte("EXISTING=value without newline")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GRIPI_ENV_PATH", path)

	if err := ensurePassword(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`\AEXISTING=value without newline\nGRIPI_ADMIN_PASSWORD=[0-9a-f]{24}\n\z`)
	if !pattern.Match(contents) {
		t.Fatalf("env contents = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}

	before := append([]byte(nil), contents...)
	if err := ensurePassword(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("existing password file was rewritten: before %q, after %q", before, after)
	}
}

func TestEnsurePasswordRejectsEmptyConfiguredValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte("OTHER=value\n  GRIPI_ADMIN_PASSWORD=\"\"  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GRIPI_ENV_PATH", path)

	if err := ensurePassword(); err == nil || !strings.Contains(err.Error(), "GRIPI_ADMIN_PASSWORD is empty") {
		t.Fatalf("ensurePassword error = %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "OTHER=value\n  GRIPI_ADMIN_PASSWORD=\"\"  \n" {
		t.Fatalf("empty password file changed: %q", contents)
	}
}

func TestConcurrentEnsurePasswordSelectsOneAuthoritativeValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "env")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GRIPI_ENV_PATH", path)

	const attempts = 12
	errors := make(chan error, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			errors <- ensurePassword()
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)^GRIPI_ADMIN_PASSWORD=([0-9a-f]{24})$`).FindAllSubmatch(contents, -1)
	if len(matches) != 1 {
		t.Fatalf("password entries = %d in %q", len(matches), contents)
	}
}

func TestEnsurePasswordRequiresHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("GRIPI_ENV_PATH", "")
	if err := ensurePassword(); err == nil {
		t.Fatal("ensurePassword succeeded without HOME")
	}
}
