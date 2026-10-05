package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

const listLimit = 20

const sessionFields = `Each session has:
  id          Session ID; commands accept any unique prefix of it
  path        Session file; commands accept it wherever they take an ID
  name        Session name, or its first message when it has no name
  cwd         Project directory
  state       working     Pi is running a turn
              compacting  Pi is compacting the context
              waiting     Pi asked a question that someone must answer in the browser
              external    Pi CLI is using the session; the gateway only follows it
              conflict    the session file changed in a way the gateway cannot follow
              idle        nothing is running
  unread      true when a reply finished that nobody has opened in the browser
  pinned      true when the session is pinned in the sidebar
  tags        Tags assigned in the browser
  updated_at  Time of the latest message
  last_reply  First 180 characters of the latest assistant reply
`

const sessionExitCodes = `  0  success
  1  the gateway is not running, or it refused the request
  2  usage error
`

const listHelp = `Usage:
  gripi list [--all] [--json]

Prints the 20 most recently active sessions, newest first.

Flags:
  --all   List every session instead of the latest 20
  --json  Print a JSON array instead of a table

` + sessionFields + `
Examples:
  gripi list
  gripi list --all --json | jq -r '.[] | select(.state == "working") | .id'

Exit codes:
` + sessionExitCodes

// gatewayClient reaches the running gateway through its private socket.
type gatewayClient struct {
	http *http.Client
}

func newGatewayClient() (*gatewayClient, error) {
	socket, err := config.SocketPath(os.Environ())
	if err != nil {
		return nil, err
	}
	return &gatewayClient{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}}, nil
}

func (client *gatewayClient) do(request *http.Request, result any) error {
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	if err != nil {
		var wrapped *url.Error
		if errors.As(err, &wrapped) {
			err = wrapped.Err
		}
		return fmt.Errorf("cannot reach the gateway (%w); start it with 'gripi serve'. A gateway in multi-user mode accepts no commands", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &failure) != nil || failure.Error == "" {
			failure.Error = strings.TrimSpace(string(body))
		}
		return fmt.Errorf("the gateway answered %d: %s", response.StatusCode, failure.Error)
	}
	return json.Unmarshal(body, result)
}

// sessions lists every session, or just the one with the given path.
func (client *gatewayClient) sessions(only string) ([]gateway.LocalSession, error) {
	target := "http://gripi/sessions"
	if only != "" {
		target += "?session=" + url.QueryEscape(only)
	}
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Sessions []gateway.LocalSession `json:"sessions"`
	}
	return payload.Sessions, client.do(request, &payload)
}

func listSessions(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	all := flags.Bool("all", false, "")
	asJSON := flags.Bool("json", false, "")
	if positional, err := parseArguments(flags, arguments); err != nil {
		return usageError(stderr, "list", err.Error())
	} else if len(positional) > 0 {
		return usageError(stderr, "list", "takes no arguments besides flags")
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "list", err)
	}
	sessions, err := client.sessions("")
	if err != nil {
		return failure(stderr, "list", err)
	}
	if total := len(sessions); !*all && total > listLimit {
		sessions = sessions[:listLimit]
		fmt.Fprintf(stderr, "Showing the latest %d of %d sessions; pass --all for the rest.\n", listLimit, total)
	}
	if *asJSON {
		return printJSON(stdout, stderr, "list", sessions)
	}
	printSessions(stdout, sessions)
	return 0
}

// parseArguments accepts flags before, between and after positional arguments; "--" ends the flags.
func parseArguments(flags *flag.FlagSet, arguments []string) ([]string, error) {
	flags.SetOutput(io.Discard)
	var literal []string
	if index := slices.Index(arguments, "--"); index >= 0 {
		arguments, literal = arguments[:index], arguments[index+1:]
	}
	var positional []string
	for len(arguments) > 0 {
		if err := flags.Parse(arguments); err != nil {
			return nil, err
		}
		if arguments = flags.Args(); len(arguments) > 0 {
			positional = append(positional, arguments[0])
			arguments = arguments[1:]
		}
	}
	return append(positional, literal...), nil
}

func failure(stderr io.Writer, name string, err error) int {
	fmt.Fprintf(stderr, "gripi %s: %v\n", name, err)
	return exitFailure
}

func printJSON(stdout, stderr io.Writer, name string, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return failure(stderr, name, err)
	}
	return 0
}

func printSessions(stdout io.Writer, sessions []gateway.LocalSession) {
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tSTATE\tUNREAD\tUPDATED\tPROJECT\tNAME")
	for _, session := range sessions {
		id, unread := session.ID, "-"
		// A session that has not produced its first reply has no ID yet.
		if id == "" {
			id = "-"
		}
		if session.Unread {
			unread = "yes"
		}
		name := []rune(strings.Join(strings.Fields(session.Name), " "))
		if len(name) > 60 {
			name = append(name[:59], '…')
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", id, session.State, unread, age(time.Since(session.UpdatedAt)), filepath.Base(session.CWD), string(name))
	}
	table.Flush()
}

func age(elapsed time.Duration) string {
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(elapsed.Seconds())))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%dh", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%dd", int(elapsed.Hours()/24))
}
