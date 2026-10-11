package pi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
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
	client, err := start(func(client agent.Client, _ agent.Lifecycle) { observed <- client }, func(observer func(*rpc.Client, map[string]any)) (*rpc.Client, error) {
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

func TestPassedOnAnswersEncodeAsTheServerEncodedThem(t *testing.T) {
	var value any
	if err := json.Unmarshal([]byte(`{"z":1,"a":"<b>&\u2028","n":1e21,"list":[{"y":true,"b":null}]}`), &value); err != nil {
		t.Fatal(err)
	}
	direct, _ := json.Marshal(map[string]any{"state": value})
	passed, _ := json.Marshal(map[string]any{"state": encoded(value)})
	if string(passed) != string(direct) {
		t.Fatalf("passed on %s, server encoded %s", passed, direct)
	}
}

func TestModelAnswersThatPiCannotConfirmAreInvalid(t *testing.T) {
	model := map[string]any{"provider": "e2e", "id": "fixture"}
	for _, test := range []struct {
		name    string
		answers map[string]map[string]any
		check   func(*Client) error
	}{
		{"refused state", map[string]map[string]any{"get_state": {"success": false}, "get_available_models": {"success": true, "data": map[string]any{"models": []any{}}}}, func(client *Client) error {
			_, err := client.ModelSettings(context.Background())
			return err
		}},
		{"no models", map[string]map[string]any{"get_state": {"success": true, "data": map[string]any{}}, "get_available_models": {"success": true, "data": map[string]any{}}}, func(client *Client) error {
			_, err := client.ModelSettings(context.Background())
			return err
		}},
		{"unconfirmed model", map[string]map[string]any{"get_state": {"success": true, "data": map[string]any{"thinkingLevel": "high"}}}, func(client *Client) error {
			_, err := client.SetModel(context.Background(), "e2e", "fixture", "high")
			return err
		}},
		{"unconfirmed thinking", map[string]map[string]any{"get_state": {"success": true, "data": map[string]any{"model": model}}}, func(client *Client) error {
			_, err := client.SetModel(context.Background(), "e2e", "fixture", "high")
			return err
		}},
		{"unknown level", map[string]map[string]any{"cycle_thinking_level": {"success": true, "data": map[string]any{"level": "extreme"}}}, func(client *Client) error {
			_, err := client.CycleThinkingLevel(context.Background())
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.check(scriptedClient(t, test.answers)); !errors.Is(err, agent.ErrInvalidResponse) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestModelAnswersPassPiFormatsToTheBrowser(t *testing.T) {
	model := map[string]any{"provider": "e2e", "id": "fixture", "reasoning": true}
	client := scriptedClient(t, map[string]map[string]any{
		"get_state":            {"success": true, "data": map[string]any{"model": model, "thinkingLevel": "high"}},
		"get_available_models": {"success": true, "data": map[string]any{"models": []any{model}}},
		"cycle_thinking_level": {"success": true, "data": nil},
	})
	settings, err := client.ModelSettings(context.Background())
	if err != nil || string(settings.Models) != `[{"id":"fixture","provider":"e2e","reasoning":true}]` || string(settings.ScopedModels) != `[]` {
		t.Fatalf("settings = %s %s %s, %v", settings.State, settings.Models, settings.ScopedModels, err)
	}
	choice, err := client.SetModel(context.Background(), "e2e", "fixture", "high")
	if err != nil || string(choice.Model) != `{"id":"fixture","provider":"e2e","reasoning":true}` || choice.ThinkingLevel != "high" {
		t.Fatalf("choice = %s %q, %v", choice.Model, choice.ThinkingLevel, err)
	}
	if level, err := client.CycleThinkingLevel(context.Background()); err != nil || level != "" {
		t.Fatalf("level = %q, %v", level, err)
	}
}

// scriptedClient serves a session through a Pi that answers each command with its answer in answers, or else with
// success. It refuses prompts, so that requests to the extension fail at once.
func scriptedClient(t *testing.T, answers map[string]map[string]any) *Client {
	t.Helper()
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := &Client{Client: rpc.NewClient(stdinWriter, stdoutReader, nil, rpc.ClientOptions{})}
	t.Cleanup(func() { _ = client.Close(); _ = stdinReader.Close(); _ = stdoutWriter.Close() })
	go func() {
		decoder := json.NewDecoder(stdinReader)
		for {
			var command map[string]any
			if decoder.Decode(&command) != nil {
				return
			}
			answer := map[string]any{"success": command["type"] != "prompt"}
			if scripted, ok := answers[stringValue(command["type"])]; ok {
				answer = maps.Clone(scripted)
			}
			answer["type"], answer["id"], answer["command"] = "response", command["id"], command["type"]
			if json.NewEncoder(stdoutWriter).Encode(answer) != nil {
				return
			}
		}
	}()
	return client
}
