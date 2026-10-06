package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"text/tabwriter"

	"github.com/melounvitek/gripi/internal/sessions"
)

const pinHelp = `Usage:
  gripi pin <session> [--json]

Pins a session, which keeps it at the top of the browser's sidebar, then
prints the session. Pinning a pinned session changes nothing.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi pin 01a107aa

Exit codes:
` + sessionExitCodes

const unpinHelp = `Usage:
  gripi unpin <session> [--json]

Returns a pinned session to its place among the others in the browser's
sidebar, then prints the session. Unpinning a session that is not pinned
changes nothing.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
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
		var answer struct {
			Pinned bool `json:"pinned"`
		}
		if err := client.post("/sessions/pin", url.Values{"session": {session.Path}, "pinned": {strconv.FormatBool(pinned)}}, &answer); err != nil {
			return failure(stderr, name, err)
		}
		session.Pinned = answer.Pinned
		return printSession(stdout, stderr, name, session, *asJSON)
	}
}

const tagHelp = `Usage:
  gripi tag <session> <tag>... [--json]

Adds tags to a session, then prints the session. A tag is lowercased and holds
1 to 64 characters; a session has at most 32. Adding a tag that the session
already has changes nothing. Tags are added in order: if the gateway refuses
one, those before it stay.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path
  tag      One or more tags; quote a tag that has spaces

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi tag 01a107aa backend "needs review"

Exit codes:
` + sessionExitCodes

const untagHelp = `Usage:
  gripi untag <session> <tag>... [--json]

Removes tags from a session, then prints the session. Removing a tag that the
session does not have changes nothing.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path
  tag      One or more tags; quote a tag that has spaces

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi untag 01a107aa "needs review"

Exit codes:
` + sessionExitCodes

func tagSession(name string, assigned bool) func([]string, io.Reader, io.Writer, io.Writer) int {
	return func(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
		flags := flag.NewFlagSet(name, flag.ContinueOnError)
		asJSON := flags.Bool("json", false, "")
		positional, err := parseArguments(flags, arguments)
		if err != nil {
			return usageError(stderr, name, err.Error())
		}
		if len(positional) < 2 || positional[0] == "" {
			return usageError(stderr, name, "takes a session and at least one tag")
		}
		tags := positional[1:]
		// The gateway takes one tag at a time, so a bad one must be caught before any is sent.
		for _, tag := range tags {
			if _, err := sessions.NormalizeTag(tag); err != nil {
				return usageError(stderr, name, fmt.Sprintf("%q is not a tag: %v", tag, err))
			}
		}
		client, session, err := connect(positional[0])
		if err != nil {
			return failure(stderr, name, err)
		}
		for _, tag := range tags {
			var answer struct {
				Tags []string `json:"tags"`
			}
			if err := client.post("/sessions/tags", url.Values{"session": {session.Path}, "tag": {tag}, "assigned": {strconv.FormatBool(assigned)}}, &answer); err != nil {
				return failure(stderr, name, err)
			}
			session.Tags = answer.Tags
		}
		return printSession(stdout, stderr, name, session, *asJSON)
	}
}

const tagsHelp = `Usage:
  gripi tags [session] [--json]

Prints every tag with the number of sessions that have it. With a session, it
prints only the tags of that session; the numbers still count all sessions.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --json  Print a JSON array of {"name", "count"} objects instead of a table

Examples:
  gripi tags
  gripi tags 01a107aa --json | jq -r '.[].name'

Exit codes:
` + sessionExitCodes

func listTags(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("tags", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "tags", err.Error())
	}
	if len(positional) > 1 || slices.Contains(positional, "") {
		return usageError(stderr, "tags", "takes at most one session")
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "tags", err)
	}
	var payload struct {
		Tags []sessions.TagCount `json:"tags"`
	}
	if err := client.get("/tags", &payload); err != nil {
		return failure(stderr, "tags", err)
	}
	if len(positional) == 1 {
		session, err := client.session(positional[0])
		if err != nil {
			return failure(stderr, "tags", err)
		}
		payload.Tags = slices.DeleteFunc(payload.Tags, func(tag sessions.TagCount) bool { return !slices.Contains(session.Tags, tag.Name) })
	}
	if *asJSON {
		return printJSON(stdout, stderr, "tags", payload.Tags)
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "TAG\tSESSIONS")
	for _, tag := range payload.Tags {
		fmt.Fprintf(table, "%s\t%d\n", tag.Name, tag.Count)
	}
	table.Flush()
	return 0
}
