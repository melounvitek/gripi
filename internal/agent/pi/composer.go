package pi

import (
	"slices"
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

var builtinCommands = []agent.Command{
	{Name: "name", Group: "other", Description: "Set session display name"},
	{Name: "compact", Group: "other", Description: "Manually compact context, optional custom instructions"},
	{Name: "reload", Group: "other", Description: "Reload Pi extensions, skills, prompts, themes, and context files"},
	{Name: "export", Group: "other", Description: "Download this session as HTML, with an optional filename"},
	{Name: "fork", Group: "other", Description: "Open the fork picker for this session"},
	{Name: "tree", Group: "other", Description: "Navigate the current session tree"},
	{Name: "clone", Group: "other", Description: "Clone this session and switch to the clone"},
	{Name: "new", Group: "other", Description: "Start a new session in this folder"},
	{Name: "model", Group: "other", Description: "Choose the model and thinking level"},
	{Name: "login", Group: "other", Description: "Show Pi CLI login instructions"},
	{Name: "logout", Group: "other", Description: "Show Pi CLI logout instructions"},
}

func (Composer) Commands() []agent.Command { return slices.Clone(builtinCommands) }

// CommandGroups follows the sources of Pi's commands. The gateway's own commands are in the last group.
func (Composer) CommandGroups() []string { return []string{"extension", "prompt", "skill", "other"} }

var thinkingLevels = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

func (Composer) ThinkingLevels() []string { return thinkingLevels }

var treeFilters = []string{"default", "no-tools", "user-only", "labeled-only", "all"}

func (Composer) TreeFilters() []string { return treeFilters }
