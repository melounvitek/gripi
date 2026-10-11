package hermes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeHermesPath() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "e2e", "support", "fake_hermes.mjs")
}

func TestRequestsGetHermesErrorAnswers(t *testing.T) {
	conn, err := start(fakeHermesPath(), os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	err = conn.request(context.Background(), "session/close", map[string]any{"sessionId": "unknown"}, nil)
	var answer *rpcError
	if !errors.As(err, &answer) || answer.Code != -32601 {
		t.Fatalf("request = %v", err)
	}
}

func TestCloseStopsAHermesThatKeepsRunningAndWhatItStarted(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "hermes")
	childFile := filepath.Join(directory, "child")
	script := "#!/bin/sh\nsleep 60 &\necho $! > '" + childFile + "'\nwait\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	conn, err := start(path, os.Environ(), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.grace = 50 * time.Millisecond
	var child int
	for deadline := time.Now().Add(5 * time.Second); child == 0 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		contents, _ := os.ReadFile(childFile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(contents)))
	}
	if child == 0 {
		t.Fatal("Hermes did not start its child")
	}

	closed := make(chan struct{})
	go func() {
		_ = conn.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
	// The child's parent has gone, so nothing may reap it at once.
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(child, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if state, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child), "stat")); strings.Contains(string(state), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Hermes's child %d still runs", child)
		}
	}
	if err := conn.request(context.Background(), "initialize", nil, nil); !strings.Contains(err.Error(), "Hermes exited") {
		t.Fatalf("request after Close = %v", err)
	}
}
