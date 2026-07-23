package rpc

import "slices"

type Command map[string]any

var builtinCommands = []Command{
	{"name": "name", "source": "other", "description": "Set session display name"},
	{"name": "compact", "source": "other", "description": "Manually compact context, optional custom instructions"},
	{"name": "export", "source": "other", "description": "Download this session as HTML, with an optional filename"},
	{"name": "fork", "source": "other", "description": "Open the fork picker for this session"},
	{"name": "tree", "source": "other", "description": "Navigate the current session tree"},
	{"name": "clone", "source": "other", "description": "Clone this session and switch to the clone"},
	{"name": "new", "source": "other", "description": "Start a new session in this folder"},
	{"name": "model", "source": "other", "description": "Choose the model and thinking level"},
	{"name": "login", "source": "other", "description": "Show Pi CLI login instructions"},
	{"name": "logout", "source": "other", "description": "Show Pi CLI logout instructions"},
}

var internalCommandNames = []string{"gripi_tree_navigate", "gripi_tree_snapshot", "gripi_tree_leaf", "gripi_tree_label"}

func BuiltinCommands() []Command {
	result := make([]Command, len(builtinCommands))
	for index, command := range builtinCommands {
		result[index] = Command(cloneMap(command))
	}
	return result
}

func CommandsFrom(response map[string]any) []Command {
	result := BuiltinCommands()
	seen := make(map[string]bool, len(result))
	for _, command := range result {
		seen[stringValue(command["name"])] = true
	}
	data := response
	if nested, ok := response["data"].(map[string]any); ok {
		data = nested
	}
	raw, ok := data["commands"].([]any)
	if !ok {
		return result
	}
	for _, item := range raw {
		command, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := stringValue(command["name"])
		if name == "" || seen[name] || slices.Contains(internalCommandNames, name) {
			continue
		}
		seen[name] = true
		result = append(result, Command(cloneMap(command)))
	}
	return result
}
