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

const overview = `Gripi serves Pi coding-agent sessions in the browser. This command starts the
gateway and lets scripts and agents work with its sessions.

Usage:
  gripi <command> [arguments]

Session commands (need a running gateway on this machine):
  list      List sessions with their state
  send      Send a message to a session
  wait      Wait until a session stops working
  pin       Pin a session in the browser's sidebar
  unpin     Unpin a session
  tag       Add tags to a session
  untag     Remove tags from a session
  tags      List tags and how many sessions have each

Gateway commands:
  serve     Start the gateway
  password  Create the admin password if it is missing

Run 'gripi help <command>' or 'gripi <command> --help' for details.
`

type command struct {
	name    string
	summary string
	// help follows the summary and starts with the usage section.
	help string
	run  func(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int
}

var commands = []command{
	{name: "list", summary: "List sessions with their state", run: listSessions, help: listHelp},
	{name: "send", summary: "Send a message to a session", run: sendMessage, help: sendHelp},
	{name: "wait", summary: "Wait until a session stops working", run: waitForSession, help: waitHelp},
	{name: "pin", summary: "Pin a session in the browser's sidebar", run: pinSession("pin", true), help: pinHelp},
	{name: "unpin", summary: "Unpin a session", run: pinSession("unpin", false), help: unpinHelp},
	{name: "tag", summary: "Add tags to a session", run: tagSession("tag", true), help: tagHelp},
	{name: "untag", summary: "Remove tags from a session", run: tagSession("untag", false), help: untagHelp},
	{name: "tags", summary: "List tags and how many sessions have each", run: listTags, help: tagsHelp},
	{name: "serve", summary: "Start the gateway", run: withoutArguments("serve", serve), help: `Usage:
  gripi serve

Runs the web gateway until it is interrupted. Settings come from environment
variables and ~/.config/gripi/env; docs/configuration.md lists them. The
gateway listens on 127.0.0.1:4567 unless GRIPI_BIND_HOST or GRIPI_PORT say
otherwise.

An installed gateway is started with ~/.local/share/gripi/bin/start instead,
which also restarts it after an update.

Example:
  GRIPI_PORT=8080 gripi serve
`},
	{name: "password", summary: "Create the admin password if it is missing", run: withoutArguments("password", ensurePassword), help: `Usage:
  gripi password

Adds a random GRIPI_ADMIN_PASSWORD to ~/.config/gripi/env (or the file named
by GRIPI_ENV_PATH) and prints it. Does nothing when a password is already set.
`},
}

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) > 0 && arguments[0] == "help" {
		arguments = arguments[1:]
		if len(arguments) > 0 && !helpFlag(arguments[0]) {
			arguments = []string{arguments[0], "--help"}
		}
	}
	if len(arguments) == 0 || helpFlag(arguments[0]) {
		fmt.Fprint(stdout, overview)
		return 0
	}
	name, rest := arguments[0], arguments[1:]
	for _, candidate := range commands {
		if candidate.name != name {
			continue
		}
		for _, argument := range rest {
			if argument == "--" {
				break
			}
			if helpFlag(argument) {
				fmt.Fprintf(stdout, "%s.\n\n%s", candidate.summary, candidate.help)
				return 0
			}
		}
		return candidate.run(rest, stdin, stdout, stderr)
	}
	fmt.Fprintf(stderr, "gripi: unknown command %q\nRun 'gripi help' for the list of commands.\n", name)
	return exitUsage
}

func helpFlag(argument string) bool {
	return argument == "-h" || argument == "-help" || argument == "--help"
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
