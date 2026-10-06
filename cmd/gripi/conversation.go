package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"

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
