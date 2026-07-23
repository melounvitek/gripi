package prompts

import "testing"

func TestParseBashCommandPreservesNativeBangModes(t *testing.T) {
	tests := []struct {
		message, mode, command string
		excluded, ok           bool
	}{
		{"!  printf 'one\\ntwo'\n| cat  ", "", "printf 'one\\ntwo'\n| cat", false, true},
		{"!! git status ", "", "git status", true, true},
		{" !pwd", "", "", false, false},
		{"!  ", "", "", false, false},
		{"!pwd", "prompt", "", false, false},
	}
	for _, test := range tests {
		result, ok := ParseBashCommand(test.message, test.mode)
		if ok != test.ok || result.Command != test.command || result.ExcludeFromContext != test.excluded {
			t.Errorf("ParseBashCommand(%q, %q) = %#v, %v", test.message, test.mode, result, ok)
		}
	}
}

func TestParseSlashCommandRecognizesOnlyCompleteControlCommands(t *testing.T) {
	tests := map[string]SlashCommand{
		" /name Release plan ": {Type: "name", Name: "Release plan"},
		"/name":                {Type: "name"},
		"/compact keep APIs":   {Type: "compact", Instructions: "keep APIs"},
		"/compact":             {Type: "compact"},
		"/fork":                {Type: "fork"},
		"/tree":                {Type: "tree"},
		"/clone":               {Type: "clone"},
		"/new":                 {Type: "new"},
		"/model":               {Type: "model"},
		"/login":               {Type: "login"},
		"/login anthropic":     {Type: "login"},
		"/logout":              {Type: "logout"},
		"/compact\nkeep APIs":  {},
		"/name\nRelease":       {},
		"/login\nanthropic":    {},
		"/login\tanthropic":    {},
		"/logout anthropic":    {},
		"/logins":              {},
		"/logouts":             {},
		"/fork later":          {},
	}
	for message, expected := range tests {
		if result := ParseSlashCommand(message); result != expected {
			t.Errorf("ParseSlashCommand(%q) = %#v, want %#v", message, result, expected)
		}
	}
}
