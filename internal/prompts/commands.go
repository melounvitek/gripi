package prompts

import "strings"

type BashCommand struct {
	Command            string
	ExcludeFromContext bool
}

func ParseBashCommand(message, mode string) (BashCommand, bool) {
	if mode == "prompt" || !strings.HasPrefix(message, "!") {
		return BashCommand{}, false
	}
	excluded := strings.HasPrefix(message, "!!")
	prefix := "!"
	if excluded {
		prefix = "!!"
	}
	command := strings.TrimSpace(strings.TrimPrefix(message, prefix))
	if command == "" {
		return BashCommand{}, false
	}
	return BashCommand{Command: command, ExcludeFromContext: excluded}, true
}

type SlashCommand struct {
	Type         string
	Name         string
	Instructions string
}

func ParseSlashCommand(message string) SlashCommand {
	trimmed := strings.TrimSpace(message)
	for _, prefix := range []string{"/name", "/compact"} {
		if trimmed == prefix {
			return SlashCommand{Type: strings.TrimPrefix(prefix, "/")}
		}
		if strings.HasPrefix(trimmed, prefix) && len(trimmed) > len(prefix) && (trimmed[len(prefix)] == ' ' || trimmed[len(prefix)] == '\t') && !strings.ContainsAny(trimmed, "\r\n") {
			value := strings.TrimSpace(trimmed[len(prefix):])
			if prefix == "/name" {
				return SlashCommand{Type: "name", Name: value}
			}
			return SlashCommand{Type: "compact", Instructions: value}
		}
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return SlashCommand{}
	}
	if trimmed == "/login" || strings.HasPrefix(trimmed, "/login ") {
		return SlashCommand{Type: "login"}
	}
	if trimmed == "/logout" {
		return SlashCommand{Type: "logout"}
	}
	for _, kind := range []string{"fork", "tree", "clone", "new", "model"} {
		if trimmed == "/"+kind {
			return SlashCommand{Type: kind}
		}
	}
	return SlashCommand{}
}
