package pi

import (
	"io"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/rpc"
)

func TestCommandCatalogPreservesBuiltinsAndFiltersPrivateExtensionCommands(t *testing.T) {
	commands := append(Composer{}.Commands(), commandsFrom(map[string]any{"data": map[string]any{"commands": []any{
		map[string]any{"name": "review", "source": "skill", "description": "Review code"},
		map[string]any{"name": "compact", "source": "skill", "description": "override"},
		map[string]any{"name": "gripi_tree_snapshot", "source": "extension"},
		map[string]any{"name": "gripi_reload", "source": "extension"},
	}}})...)
	if len(commands) != 12 || commands[1] != (agent.Command{Name: "compact", Description: "Manually compact context, optional custom instructions", Group: "other"}) || commands[2].Name != "reload" || commands[3].Name != "export" || commands[9].Name != "login" || commands[10].Name != "logout" || commands[11] != (agent.Command{Name: "review", Description: "Review code", Group: "skill"}) {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestStartedClientsNameThemselvesInTheirEvents(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	t.Cleanup(func() { _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	observed := make(chan agent.Client, 1)
	client, err := start(func(client agent.Client, _ map[string]any) { observed <- client }, func(observer func(*rpc.Client, map[string]any)) (*rpc.Client, error) {
		return rpc.NewClient(stdinWriter, stdoutReader, nil, rpc.ClientOptions{EventObserver: observer}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if _, err := io.WriteString(stdoutWriter, `{"type":"agent_start"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case named := <-observed:
		if named != client {
			t.Fatalf("event named %T %p, not the started client %p", named, named, client)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not observed")
	}
}
