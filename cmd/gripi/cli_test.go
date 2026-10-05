package main

import (
	"bytes"
	"strings"
	"testing"
)

func runCLI(arguments ...string) (code int, stdout, stderr string) {
	return runCLIWithInput("", arguments...)
}

func runCLIWithInput(input string, arguments ...string) (code int, stdout, stderr string) {
	var out, errors bytes.Buffer
	code = run(arguments, strings.NewReader(input), &out, &errors)
	return code, out.String(), errors.String()
}

func TestBareCommandAndHelpFlagsPrintTheOverview(t *testing.T) {
	_, overview, _ := runCLI()
	for _, arguments := range [][]string{{}, {"help"}, {"-h"}, {"--help"}} {
		code, stdout, stderr := runCLI(arguments...)
		if code != 0 || stdout != overview || stderr != "" {
			t.Fatalf("gripi %v = %d, stdout %q, stderr %q", arguments, code, stdout, stderr)
		}
	}
	if !strings.Contains(overview, "Usage:\n  gripi <command>") {
		t.Fatalf("overview has no usage line: %q", overview)
	}
	for _, command := range commands {
		if !strings.Contains(overview, "  "+command.name) || !strings.Contains(overview, command.summary) {
			t.Fatalf("overview does not describe %q: %q", command.name, overview)
		}
	}
}

func TestEveryCommandExplainsItselfWithoutRunning(t *testing.T) {
	for _, command := range commands {
		code, help, stderr := runCLI("help", command.name)
		if code != 0 || stderr != "" {
			t.Fatalf("gripi help %s = %d, stderr %q", command.name, code, stderr)
		}
		if !strings.HasPrefix(help, command.summary) || !strings.Contains(help, "Usage:\n  gripi "+command.name) {
			t.Fatalf("gripi help %s = %q", command.name, help)
		}
		for _, flag := range []string{"-h", "--help"} {
			if code, stdout, stderr := runCLI(command.name, flag); code != 0 || stdout != help || stderr != "" {
				t.Fatalf("gripi %s %s = %d, stdout %q, stderr %q", command.name, flag, code, stdout, stderr)
			}
		}
	}
}

func TestUnknownCommandsAndStrayArgumentsFailWithAHint(t *testing.T) {
	for _, arguments := range [][]string{{"nope"}, {"help", "nope"}, {"--version"}, {"serve", "extra"}, {"password", "extra"}} {
		code, stdout, stderr := runCLI(arguments...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "gripi help") {
			t.Fatalf("gripi %v = %d, stdout %q, stderr %q", arguments, code, stdout, stderr)
		}
	}
}
