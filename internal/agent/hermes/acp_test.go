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

	"github.com/melounvitek/gripi/internal/agent"
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

func TestHermesErrorsSayWhatFailed(t *testing.T) {
	t.Setenv("FAKE_HERMES_FAIL", "session/new")
	adapter := New(Options{Path: fakeHermesPath()})
	defer adapter.Close()

	_, err := adapter.StartInCWD(t.TempDir(), "", agent.StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "cannot import name 'RESUME_REFUSALS'") {
		t.Fatalf("StartInCWD = %v", err)
	}
}

// installHermes has the fake Hermes print output for `--version`, as an installed Hermes names itself.
func installHermes(t *testing.T, output string) {
	t.Helper()
	version := os.Getenv("FAKE_HERMES_VERSION")
	if version == "" {
		version = filepath.Join(t.TempDir(), "version")
		t.Setenv("FAKE_HERMES_VERSION", version)
	}
	if err := os.WriteFile(version, []byte(output), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAMessageThatAnUpdatedHermesStopsUnderFailsAtOnce(t *testing.T) {
	installHermes(t, "Hermes Agent v1.0.0+1.gaaaaaa\n")
	adapter := New(Options{Path: fakeHermesPath()})
	defer adapter.Close()
	open, err := adapter.StartInCWD(t.TempDir(), "", agent.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer open.Close()

	// The message is still being prepared when another conversation starts in the updated Hermes.
	preparing, prepared := make(chan struct{}), make(chan struct{})
	sent := make(chan error, 1)
	go func() {
		_, err := open.Prompt(context.Background(), agent.Prompt{Text: "List the files", Images: func() ([]agent.PromptImage, error) {
			close(preparing)
			<-prepared
			return nil, nil
		}})
		sent <- err
	}()
	<-preparing
	installHermes(t, "Hermes Agent v1.0.0+2.gbbbbbb\n")
	next, err := adapter.StartInCWD(t.TempDir(), "", agent.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	close(prepared)

	if err := <-sent; !errors.Is(err, agent.ErrProcessExited) {
		t.Fatalf("the message to the stopped Hermes = %v", err)
	}
}

func TestASavedConversationOpenedTwiceAtOnceAfterAnUpdateKeepsItsHistory(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	saved := `{"sessions": [{"sessionId": "saved", "cwd": "/tmp", "title": "Saved", "updatedAt": "2026-03-01T10:00:00Z",
		"history": [{"sessionUpdate": "user_message_chunk", "content": {"type": "text", "text": "First question"}}]}]}`
	if err := os.WriteFile(state, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_HERMES_STATE", state)
	installHermes(t, "Hermes Agent v1.0.0+1.gaaaaaa\n")
	adapter := New(Options{Path: fakeHermesPath()})
	defer adapter.Close()
	store := adapter.Store()
	if _, err := store.Sessions(); err != nil {
		t.Fatal(err)
	}

	installHermes(t, "Hermes Agent v1.0.0+2.gbbbbbb\n")
	opened := make(chan struct{})
	for range 2 {
		go func() {
			_, _ = store.Window("hermes:saved", "", false, nil, nil)
			opened <- struct{}{}
		}()
	}
	<-opened
	<-opened

	window, err := store.Window("hermes:saved", "", false, nil, nil)
	if err != nil || len(window.Messages) != 1 {
		t.Fatalf("the conversation shows %d messages: %v", len(window.Messages), err)
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

func TestHermesStopsOnceIdleWithoutClients(t *testing.T) {
	adapter := New(Options{Path: fakeHermesPath(), IdleTimeout: 100 * time.Millisecond})
	defer adapter.Close()
	if _, err := adapter.Store().Sessions(); err != nil {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	conn := adapter.conn
	adapter.mu.Unlock()
	if conn == nil {
		t.Fatal("Hermes did not start to list its sessions")
	}
	select {
	case <-conn.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle Hermes still runs")
	}
}
