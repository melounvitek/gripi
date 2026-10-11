package hermes

import "github.com/melounvitek/gripi/internal/agent"

// composer sends everything the user types to Hermes, which runs its own slash commands. Hermes has no shell
// commands of the user's, and no commands that the gateway runs itself.
type composer struct{}

func (composer) InternalCommand(string) bool                    { return false }
func (composer) ShellCommand(string) (agent.ShellCommand, bool) { return agent.ShellCommand{}, false }
func (composer) SlashCommand(string) agent.SlashCommand         { return agent.SlashCommand{} }
func (composer) Commands() []agent.Command                      { return nil }
func (composer) CommandGroups() []string                        { return []string{commandGroup} }
func (composer) ThinkingLevels() []string                       { return nil }
func (composer) TreeFilters() []string                          { return nil }

// commandGroup holds Hermes's own commands in the command palette.
const commandGroup = "Hermes"
