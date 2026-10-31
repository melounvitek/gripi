package rpc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

func TestALauncherRunsPiElsewhereAndHearsWhenItEnded(t *testing.T) {
	dump, here := filepath.Join(t.TempDir(), "ran"), t.TempDir()
	var launched []agent.Process
	released := make(chan struct{})
	launch := func(process agent.Process) (*exec.Cmd, func(), error) {
		launched = append(launched, process)
		if process.Dir == here {
			return nil, nil, nil
		}
		return exec.Command("/bin/sh", "-c", `echo elsewhere > "$0"`, dump), func() { close(released) }, nil
	}

	client, err := Start("/sessions/s.jsonl", "/project", []string{"pi", "--approve"}, "/extensions/gripi-tree.ts", []string{"SAVED=1"}, launch, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the launcher was not told that its command ended")
	}
	if contents, _ := os.ReadFile(dump); string(contents) != "elsewhere\n" {
		t.Fatalf("the launcher's command wrote %q", contents)
	}
	expected := agent.Process{
		Key: "/sessions/s.jsonl", Dir: "/project", Env: []string{"SAVED=1"},
		Command: []string{"pi", "--approve", "--mode", "rpc", "--extension", "/extensions/gripi-tree.ts", "--session", "/sessions/s.jsonl"},
	}
	if !reflect.DeepEqual(launched, []agent.Process{expected}) {
		t.Fatalf("launched %+v, want %+v", launched, expected)
	}

	local, err := StartInCWD(here, "", []string{"/bin/sh", "-c", `echo here > "$0"`, dump}, "unused.ts", nil, launch, nil, nil)
	if err == nil {
		t.Cleanup(func() { _ = local.Close() })
		<-local.waitDone
	}
	if contents, _ := os.ReadFile(dump); string(contents) != "here\n" || launched[1].Key != "" {
		t.Fatalf("a session the launcher left here wrote %q, %v; launched %+v", contents, err, launched[1])
	}

	_, err = Start("/sessions/s.jsonl", "", []string{"pi"}, "unused.ts", nil, func(agent.Process) (*exec.Cmd, func(), error) {
		return nil, nil, errors.New("the worker cannot be reached")
	}, nil, nil)
	if !errors.Is(err, agent.ErrStartFailed) || !strings.Contains(err.Error(), "the worker cannot be reached") {
		t.Fatalf("a failed launch = %v", err)
	}
}
