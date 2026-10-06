package main

import (
	"flag"
	"io"
	"net/url"
	"strconv"
)

const sessionArgumentAndJSON = `
Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields

const pinHelp = `Usage:
  gripi pin <session> [--json]

Pins a session, which keeps it at the top of the browser's sidebar, then
prints the session. Pinning a pinned session changes nothing.
` + sessionArgumentAndJSON + `
Example:
  gripi pin 01a107aa

Exit codes:
` + sessionExitCodes

const unpinHelp = `Usage:
  gripi unpin <session> [--json]

Returns a pinned session to its place among the others in the browser's
sidebar, then prints the session. Unpinning a session that is not pinned
changes nothing.
` + sessionArgumentAndJSON + `
Example:
  gripi unpin 01a107aa

Exit codes:
` + sessionExitCodes

func pinSession(name string, pinned bool) func([]string, io.Reader, io.Writer, io.Writer) int {
	return func(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
		flags := flag.NewFlagSet(name, flag.ContinueOnError)
		asJSON := flags.Bool("json", false, "")
		positional, err := parseArguments(flags, arguments)
		if err != nil {
			return usageError(stderr, name, err.Error())
		}
		if len(positional) != 1 || positional[0] == "" {
			return usageError(stderr, name, "takes exactly one session")
		}
		client, session, err := connect(positional[0])
		if err != nil {
			return failure(stderr, name, err)
		}
		form := url.Values{"session": {session.Path}, "pinned": {strconv.FormatBool(pinned)}}
		// The answer carries the new "pinned", which lands on the session printed below.
		if err := client.post("/sessions/pin", form, &session); err != nil {
			return failure(stderr, name, err)
		}
		return printSession(stdout, stderr, name, session, *asJSON)
	}
}
