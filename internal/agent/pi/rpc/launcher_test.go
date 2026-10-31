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
		if process.Lost == nil {
			t.Error("the launcher cannot report that it lost Pi")
		}
		process.Lost = nil
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

// A launcher that loses Pi, such as with the connection to a worker, ends the run that was going with its message, as
// a run ends whose Pi exited, whether Pi's output ended before or after; an idle session gets no message.
func TestARunEndsWithTheMessageOfTheLauncherThatLostPi(t *testing.T) {
	const message = "Lost the connection to worker beelink. The outcome of the running step is unknown."
	for _, test := range []struct {
		name               string
		running, beforeEnd bool
	}{{"lost while Pi runs", true, true}, {"lost after Pi's output ended", true, false}, {"idle", false, false}} {
		t.Run(test.name, func(t *testing.T) {
			events := ""
			if test.running {
				events = `echo '{"type":"agent_start"}'; echo '{"type":"tool_execution_start","toolCallId":"t1","toolName":"bash","args":{}}';`
			}
			var lose func(string)
			launch := func(process agent.Process) (*exec.Cmd, func(), error) {
				lose = process.Lost
				return exec.Command("/bin/sh", "-c", events+` read line`), func() {}, nil
			}
			client, err := Start("/sessions/s.jsonl", "/project", []string{"pi"}, "gripi-tree.ts", nil, launch, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			for deadline := time.Now().Add(5 * time.Second); client.Busy() != test.running; time.Sleep(10 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("Pi did not start its run")
				}
			}
			if test.beforeEnd {
				lose(message)
			}
			_, _ = client.stdin.Write([]byte("\n"))
			<-client.waitDone
			lose(message)

			var ends []map[string]any
			for _, event := range client.EventsAfter(0).Events {
				if event["type"] == "agent_settled" {
					ends = append(ends, event)
				}
			}
			if !test.running {
				if len(ends) != 0 {
					t.Fatalf("an idle session got %v", ends)
				}
				return
			}
			if len(ends) != 1 || ends[0]["error"] != message || ends[0]["gatewayTimestamp"] == nil {
				t.Fatalf("the run ended with %v", ends)
			}
		})
	}
}
