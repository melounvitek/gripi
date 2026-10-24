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
		form := url.Values{"session": {session.Path}, "pinned": {strconv.FormatBool(pinned)}}
		if err := client.post("/sessions/pin", form, &answer); err != nil {
			return failure(stderr, name, err)
		}
		session.Pinned = answer.Pinned
		return printSession(stdout, stderr, name, session, *asJSON)
	}
}

const renameHelp = `Usage:
  gripi rename <session> <name> [--json]

Names a session, as /name does in Pi CLI and in the browser's composer, then
prints the session. The name replaces the first message as the session's
title in listings and in the browser's sidebar.

A session from 'gripi new' takes a name only once Pi has replied to its first
message. A session in state external or conflict cannot be renamed.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path
  name     The new name; quote a name that has spaces

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi rename 01a107aa "Release planning"

Exit codes:
` + sessionExitCodes

func renameSession(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("rename", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "rename", err.Error())
	}
	if len(positional) != 2 || positional[0] == "" {
		return usageError(stderr, "rename", "takes a session and one name; quote a name that has spaces")
	}
	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "rename", err)
	}
	var answer struct {
		Name string `json:"name"`
	}
	if err := client.post("/sessions/rename", url.Values{"session": {session.Path}, "name": {positional[1]}}, &answer); err != nil {
		return failure(stderr, "rename", err)
	}
	session.Name = answer.Name
	return printSession(stdout, stderr, "rename", session, *asJSON)
}

const claimHelp = `Usage:
  gripi claim <session> [--json]

Makes a session that has no owner yours, then prints it. On a gateway in
multi-user mode, a session started in Pi CLI has no owner, and no user sees it
until one claims it. Claiming your own session changes nothing, and another
user's session cannot be claimed. On a gateway without multi-user mode, every
session is already yours.

Claiming changes only the owner: Pi CLI goes on running in the session.

Arguments:
  session  Full session ID or the session file path; a prefix is not enough,
           because the session is not listed until it is claimed

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi claim 01a107aa-cfe1-70a9-b326-0f49ff3bf0da

Exit codes:
` + sessionExitCodes

func claimSession(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("claim", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "claim", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "claim", "takes exactly one session")
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "claim", err)
	}
	var answer struct {
		Session string `json:"session"`
	}
	if err := client.post("/sessions/claim", url.Values{"session": {positional[0]}}, &answer); err != nil {
		return failure(stderr, "claim", err)
	}
	session, err := client.session(answer.Session)
	if err != nil {
		return failure(stderr, "claim", err)
	}
	return printSession(stdout, stderr, "claim", session, *asJSON)
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
			form := url.Values{"session": {session.Path}, "tag": {tag}, "assigned": {strconv.FormatBool(assigned)}}
			if err := client.post("/sessions/tags", form, &answer); err != nil {
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
