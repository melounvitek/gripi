package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"

	gateway "github.com/melounvitek/gripi/internal/server"
)

const showHelp = `Usage:
  gripi show <session> [--all] [--json]

Prints the latest reply of a session in full. Run it after 'gripi wait' to
read what Pi answered.

With --all it prints the recent conversation instead: each message under its
role, and each tool call on one line. A long conversation starts at a recent
message; the session file has all of it.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --all   Print the recent conversation instead of only the latest reply
  --json  Print the messages as a JSON array instead of text

Message fields:
  role       user       a message sent to Pi
             assistant  text that Pi replied with
             tool       a tool call; text has the tool's name or the shell
                        command, never the output
             status     a compaction of the context
             error      an error that ended a turn
             custom     a message that an extension shows
  text       The whole message
  timestamp  Time the message was written

Examples:
  gripi send 01a107aa "Run the tests" && gripi wait 01a107aa && gripi show 01a107aa
  gripi show 01a107aa --all --json | jq -r '.[] | select(.role == "user") | .text'

Exit codes:
` + sessionExitCodes

func showConversation(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("show", flag.ContinueOnError)
	all := flags.Bool("all", false, "")
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "show", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "show", "takes exactly one session")
	}
	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "show", err)
	}
	var conversation struct {
		Messages []gateway.LocalMessage `json:"messages"`
	}
	if err := client.get("/conversation?session="+url.QueryEscape(session.Path), &conversation); err != nil {
		return failure(stderr, "show", err)
	}
	messages := conversation.Messages
	if !*all {
		messages = []gateway.LocalMessage{}
		for _, message := range conversation.Messages {
			if message.Role == "assistant" {
				messages = []gateway.LocalMessage{message}
			}
		}
	}
	if *asJSON {
		return printJSON(stdout, stderr, "show", messages)
	}
	if len(messages) == 0 {
		fmt.Fprintln(stderr, "gripi show: the session has no reply yet")
	}
	for index, message := range messages {
		switch {
		case !*all:
			fmt.Fprintln(stdout, message.Text)
		case message.Role == "tool" || message.Role == "status":
			fmt.Fprintf(stdout, "[%s] %s\n", message.Role, strings.Join(strings.Fields(message.Text), " "))
		default:
			fmt.Fprintf(stdout, "[%s]\n%s\n", message.Role, message.Text)
		}
		if index < len(messages)-1 {
			fmt.Fprintln(stdout)
		}
	}
	return 0
}

const openHelp = `Usage:
  gripi open <session>

Continues a session in Pi CLI. It changes to the session's project directory
and replaces itself with 'pi --session <path>', so it needs a terminal and
does not return until Pi CLI exits.

The gateway must not be running the session: its state has to be idle,
external or conflict. Once Pi CLI has written to the session, the gateway only
follows it (state external) until someone takes it over in the browser.

Pi CLI starts as it does for any session. It does not get the environment
variables saved in Gripi, and it asks before it trusts a project.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Example:
  gripi open 01a107aa

Exit codes:
  1  the gateway cannot be reached, no single session matched, the gateway is
     running the session, there is no terminal, or pi is not on PATH
  2  usage error
`

// Tests exchange this, because they have to outlive the command.
var replaceProcess = syscall.Exec

func openSession(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	positional, err := parseArguments(flag.NewFlagSet("open", flag.ContinueOnError), arguments)
	if err != nil {
		return usageError(stderr, "open", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "open", "takes exactly one session")
	}
	// Redirected, Pi CLI would not open its interface but run once on whatever stdin holds.
	for _, stream := range []any{stdin, stdout} {
		if file, ok := stream.(*os.File); ok {
			if info, err := file.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
				return failure(stderr, "open", errors.New("Pi CLI needs a terminal for its interface"))
			}
		}
	}
	_, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "open", err)
	}
	switch session.State {
	case "working", "compacting", "waiting":
		return failure(stderr, "open", fmt.Errorf("the gateway is running the session (%s); Pi CLI can continue it once that has ended", session.State))
	}
	pi, err := exec.LookPath("pi")
	if err != nil {
		return failure(stderr, "open", errors.New("pi is not on PATH"))
	}
	if err := os.Chdir(session.CWD); err != nil {
		return failure(stderr, "open", err)
	}
	if err := replaceProcess(pi, []string{"pi", "--session", session.Path}, os.Environ()); err != nil {
		return failure(stderr, "open", err)
	}
	return 0
}
