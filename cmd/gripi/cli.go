package main

import (
	"fmt"
	"io"
)

const (
	exitFailure = 1
	exitUsage   = 2
	exitTimeout = 3
)

const (
	sessionCommands = "Session commands (need a running gateway on this machine)"
	gatewayCommands = "Gateway commands"
)

type command struct {
	name    string
	group   string
	summary string
	// help follows the summary and starts with the usage section.
	help string
	run  func(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int
}

var commands = []command{
	{name: "list", group: sessionCommands, summary: "List sessions with their state", run: listSessions, help: listHelp},
	{name: "send", group: sessionCommands, summary: "Send a message to a session", run: sendMessage, help: sendHelp},
	{name: "wait", group: sessionCommands, summary: "Wait until a session stops working", run: waitForSession, help: waitHelp},
	{name: "serve", group: gatewayCommands, summary: "Start the gateway", run: withoutArguments("serve", serve), help: `Usage:
  gripi serve

Runs the web gateway until it is interrupted. Settings come from environment
variables and ~/.config/gripi/env; docs/configuration.md lists them. The
gateway listens on 127.0.0.1:4567 unless GRIPI_BIND_HOST or GRIPI_PORT say
otherwise.

Example:
  GRIPI_PORT=8080 gripi serve
`},
	{name: "password", group: gatewayCommands, summary: "Create the admin password if it is missing", run: withoutArguments("password", ensurePassword), help: `Usage:
  gripi password

Adds a random GRIPI_ADMIN_PASSWORD to ~/.config/gripi/env (or the file named
by GRIPI_ENV_PATH) and prints it. Does nothing when a password is already set.
`},
}

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) == 0 || helpFlag(arguments[0]) {
		printOverview(stdout)
		return 0
	}
	name, rest := arguments[0], arguments[1:]
	if name == "help" {
		if len(rest) == 0 {
			printOverview(stdout)
			return 0
		}
		name, rest = rest[0], []string{"--help"}
	}
	for _, command := range commands {
		if command.name != name {
			continue
		}
		for _, argument := range rest {
			if argument == "--" {
				break
			}
			if helpFlag(argument) {
				fmt.Fprintf(stdout, "%s.\n\n%s", command.summary, command.help)
				return 0
			}
		}
		return command.run(rest, stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "gripi: unknown command %q\nRun 'gripi help' for the list of commands.\n", name)
	return exitUsage
}

func helpFlag(argument string) bool {
	return argument == "-h" || argument == "--help"
}

func printOverview(stdout io.Writer) {
	fmt.Fprint(stdout, `Gripi serves Pi coding-agent sessions in the browser. This command starts the
gateway and lets scripts and agents work with its sessions.

Usage:
  gripi <command> [arguments]
`)
	for _, group := range []string{sessionCommands, gatewayCommands} {
		header := "\n" + group + ":\n"
		for _, command := range commands {
			if command.group == group {
				fmt.Fprintf(stdout, "%s  %-10s%s\n", header, command.name, command.summary)
				header = ""
			}
		}
	}
	fmt.Fprint(stdout, "\nRun 'gripi help <command>' or 'gripi <command> --help' for details.\n")
}

func usageError(stderr io.Writer, name, message string) int {
	fmt.Fprintf(stderr, "gripi %s: %s\nRun 'gripi help %s' for usage.\n", name, message, name)
	return exitUsage
}

func withoutArguments(name string, action func() error) func([]string, io.Reader, io.Writer, io.Writer) int {
	return func(arguments []string, _ io.Reader, _, stderr io.Writer) int {
		if len(arguments) > 0 {
			return usageError(stderr, name, "takes no arguments")
		}
		if err := action(); err != nil {
			fmt.Fprintf(stderr, "gripi %s: %v\n", name, err)
			return exitFailure
		}
		return 0
	}
}
