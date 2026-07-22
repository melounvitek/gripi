package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

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
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode = %o", info.Mode().Perm())
		}
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
