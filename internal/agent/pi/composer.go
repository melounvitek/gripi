package pi

import (
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/rpc"
)

// Composer reads what the user types the way Pi CLI's editor does.
type Composer struct{}

var _ agent.Composer = Composer{}

func (Composer) InternalCommand(message string) bool {
	return rpc.InternalCommand(message)
}

// ShellCommand reads `!command`, and `!!command`, which Pi leaves out of the model's context.
func (Composer) ShellCommand(message string) (agent.ShellCommand, bool) {
	if !strings.HasPrefix(message, "!") {
		return agent.ShellCommand{}, false
	}
	excluded := strings.HasPrefix(message, "!!")
	prefix := "!"
	if excluded {
		prefix = "!!"
	}
	command := strings.TrimSpace(strings.TrimPrefix(message, prefix))
	if command == "" {
		return agent.ShellCommand{}, false
	}
	return agent.ShellCommand{Command: command, ExcludeFromContext: excluded}, true
}

func (Composer) SlashCommand(message string) agent.SlashCommand {
	trimmed := strings.TrimSpace(message)
	for _, name := range []string{"name", "compact"} {
		prefix := "/" + name
		if trimmed == prefix {
			return agent.SlashCommand{Name: name}
		}
		if strings.HasPrefix(trimmed, prefix) && len(trimmed) > len(prefix) && (trimmed[len(prefix)] == ' ' || trimmed[len(prefix)] == '\t') && !strings.ContainsAny(trimmed, "\r\n") {
			return agent.SlashCommand{Name: name, Argument: strings.TrimSpace(trimmed[len(prefix):])}
		}
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return agent.SlashCommand{}
	}
	if trimmed == "/login" || strings.HasPrefix(trimmed, "/login ") {
		return agent.SlashCommand{Name: "login", Unavailable: "`/login` isn’t available in Gripi. Run `/login` in the Pi CLI, then restart the Gripi gateway to load the new credentials."}
	}
	if trimmed == "/logout" {
		return agent.SlashCommand{Name: "logout", Unavailable: "`/logout` isn’t available in Gripi. Run `/logout` in the Pi CLI, then restart the Gripi gateway to reload credentials."}
	}
	for _, name := range []string{"fork", "tree", "clone", "new", "model", "reload"} {
		if trimmed == "/"+name {
			return agent.SlashCommand{Name: name}
		}
	}
	return agent.SlashCommand{}
}

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func (Composer) ThinkingLevels() []string { return thinkingLevels }

var treeFilters = []string{"default", "no-tools", "user-only", "labeled-only", "all"}

func (Composer) TreeFilters() []string { return treeFilters }
