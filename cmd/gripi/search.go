package main

import (
	"flag"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gateway "github.com/melounvitek/gripi/internal/server"
)

const searchHelp = `Usage:
  gripi search <text> [filters] [--role <role>] [--all] [--json]

Finds the sessions whose conversation holds the text, ignoring case, and
prints the 20 most recently active of them, newest first by updated_at, each
with how many messages match and the latest 3 of them, cut around the text.
Use it to find out whether a session already works on a task, such as by its
ticket number, branch or pull request URL.

It searches what the user sent, Pi's replies, its tool calls with their
arguments and their results, the output of shell commands and the messages of
extensions, also on the branches that /tree left behind. It leaves out
thinking and images, and Hermes sessions. The gateway reads the session files
for each search, so filters that leave fewer sessions make it faster.

Arguments:
  text  The text to find; quote text that has spaces

` + sessionFilterHelp + `
  --role <role>    Only matches in messages with this role: user, assistant,
                   tool or custom; repeat it or separate roles with commas

Flags:
  --all   Print every matching session instead of the latest 20
  --json  Print a JSON array instead of text

No match is not an error: nothing is printed, or an empty JSON array.

Fields in JSON are those of 'gripi list' and these:
  match_count  How many messages match
  matches      The latest 3 of them, with role, timestamp and text, which
               has the text around the match on one line

Examples:
  gripi search 1209876543 --since 30d
  gripi search "fix/login_redirect" --cwd ~/Work/project --json | jq -r '.[].id'

Exit codes:
` + sessionExitCodes

var searchRoles = []string{"user", "assistant", "tool", "custom"}

// foundSession is how gripi search prints a session that holds the text.
type foundSession struct {
	gateway.LocalSession
	MatchCount int                    `json:"match_count"`
	Matches    []gateway.LocalMessage `json:"matches"`
}

func searchSessions(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	all := flags.Bool("all", false, "")
	asJSON := flags.Bool("json", false, "")
	filters := addSessionFilters(flags)
	var roles []string
	flags.Func("role", "", func(value string) error {
		for role := range strings.SplitSeq(value, ",") {
			if !slices.Contains(searchRoles, role) {
				return fmt.Errorf("%q is not a role; roles are %s", role, strings.Join(searchRoles, ", "))
			}
			roles = append(roles, role)
		}
		return nil
	})
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "search", err.Error())
	}
	if len(positional) != 1 {
		return usageError(stderr, "search", "takes exactly one text; quote text that has spaces")
	}
	text := strings.TrimSpace(positional[0])
	if text == "" {
		return usageError(stderr, "search", "the text is empty")
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "search", err)
	}
	sessions, err := client.sessions("")
	if err != nil {
		return failure(stderr, "search", err)
	}
	if sessions, err = filters.keep(sessions); err != nil {
		return failure(stderr, "search", err)
	}
	form := url.Values{"q": {text}, "role": roles}
	for _, session := range sessions {
		form.Add("session", session.Path)
	}
	var searched struct {
		Results []gateway.LocalSearchResult `json:"results"`
	}
	if len(sessions) > 0 {
		if err := client.post("/search", form, &searched); err != nil {
			return failure(stderr, "search", err)
		}
	}
	found := []foundSession{}
	for _, session := range sessions {
		index := slices.IndexFunc(searched.Results, func(result gateway.LocalSearchResult) bool { return result.Session == session.Path })
		if index >= 0 {
			found = append(found, foundSession{LocalSession: session, MatchCount: searched.Results[index].Count, Matches: searched.Results[index].Matches})
		}
	}
	total := len(found)
	if !*all && total > listLimit {
		found = found[:listLimit]
	}
	if *asJSON {
		return printJSON(stdout, stderr, "search", found)
	}
	for index, session := range found {
		if index > 0 {
			fmt.Fprintln(stdout)
		}
		name := []rune(strings.Join(strings.Fields(session.Name), " "))
		if len(name) > 60 {
			name = append(name[:59], '…')
		}
		matches := "matches"
		if session.MatchCount == 1 {
			matches = "match"
		}
		fmt.Fprintf(stdout, "%s  %s  %s  %s  %s  (%d %s)\n", session.ID, session.State, age(time.Since(session.ActivityAt.Time)), filepath.Base(session.CWD), string(name), session.MatchCount, matches)
		for _, match := range session.Matches {
			fmt.Fprintf(stdout, "  [%s] %s\n", match.Role, match.Text)
		}
	}
	if len(found) < total {
		fmt.Fprintf(stderr, "Showing the latest %d of %d matching sessions; pass --all for the rest.\n", listLimit, total)
	}
	return 0
}
