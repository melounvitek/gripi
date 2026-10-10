package rpc

import "testing"

func TestCommandCatalogPreservesBuiltinsAndFiltersPrivateExtensionCommands(t *testing.T) {
	commands := CommandsFrom(map[string]any{"data": map[string]any{"commands": []any{
		map[string]any{"name": "review", "source": "skill", "description": "Review code"},
		map[string]any{"name": "compact", "source": "skill", "description": "override"},
		map[string]any{"name": "gripi_tree_snapshot", "source": "extension"},
		map[string]any{"name": "gripi_reload", "source": "extension"},
	}}})
	if len(commands) != 12 || commands[1]["name"] != "compact" || commands[1]["description"] != "Manually compact context, optional custom instructions" || commands[2]["name"] != "reload" || commands[3]["name"] != "export" || commands[9]["name"] != "login" || commands[10]["name"] != "logout" || commands[11]["name"] != "review" {
		t.Fatalf("commands = %#v", commands)
	}
}
