package pi

import (
	"testing"

	"github.com/melounvitek/gripi/internal/agent"
)

func TestShellCommandsPreserveNativeBangModes(t *testing.T) {
	tests := []struct {
		message, command string
		excluded, ok     bool
	}{
		{"!  printf 'one\\ntwo'\n| cat  ", "printf 'one\\ntwo'\n| cat", false, true},
		{"!! git status ", "git status", true, true},
		{" !pwd", "", false, false},
		{"!  ", "", false, false},
	}
	for _, test := range tests {
		result, ok := Composer{}.ShellCommand(test.message)
		if ok != test.ok || result.Command != test.command || result.ExcludeFromContext != test.excluded {
			t.Errorf("ShellCommand(%q) = %#v, %v", test.message, result, ok)
		}
	}
}

func TestSlashCommandsRecognizeOnlyCompleteControlCommands(t *testing.T) {
	login := "`/login` isn’t available in Gripi. Run `/login` in the Pi CLI, then restart the Gripi gateway to load the new credentials."
	tests := map[string]agent.SlashCommand{
		" /name Release plan ": {Name: "name", Argument: "Release plan"},
		"/name":                {Name: "name"},
		"/compact keep APIs":   {Name: "compact", Argument: "keep APIs"},
		"/compact":             {Name: "compact"},
		"/fork":                {Name: "fork"},
		"/tree":                {Name: "tree"},
		"/clone":               {Name: "clone"},
		"/new":                 {Name: "new"},
		"/model":               {Name: "model"},
		"/reload":              {Name: "reload"},
		"/login":               {Name: "login", Unavailable: login},
		"/login anthropic":     {Name: "login", Unavailable: login},
		"/logout":              {Name: "logout", Unavailable: "`/logout` isn’t available in Gripi. Run `/logout` in the Pi CLI, then restart the Gripi gateway to reload credentials."},
		"/compact\nkeep APIs":  {},
		"/name\nRelease":       {},
		"/login\nanthropic":    {},
		"/login\tanthropic":    {},
		"/logout anthropic":    {},
		"/logins":              {},
		"/logouts":             {},
		"/fork later":          {},
		"/reload now":          {},
	}
	for message, expected := range tests {
		if result := (Composer{}).SlashCommand(message); result != expected {
			t.Errorf("SlashCommand(%q) = %#v, want %#v", message, result, expected)
		}
	}
}
