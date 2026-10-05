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

func listSessions(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
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

const sendHelp = `Usage:
  gripi send <session> [message] [--steer] [--json]

Sends a message to a session as a prompt, then prints the session. It returns
once Pi has taken the message and does not wait for the reply; use 'gripi wait'
for that.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path
  message  The prompt. Leave it out, or pass -, to read it from stdin.
           Put -- before a message that starts with a dash.

Flags:
  --steer  Deliver the message during the running turn instead of after it
  --json   Print the session as a JSON object instead of a table

While Pi is working, the message is queued and delivered once the agent has
finished. With --steer it is delivered sooner: after the tool calls of the
current step and before Pi's next request to the model. With --steer the
gateway also acts on its own slash commands such as /compact or /new, as the
browser's composer does; otherwise they reach Pi as plain text.

The text is never run as a "!" shell command. Pi still expands its own skills,
prompt templates and extension commands, such as /skill:name.

A session in state external or conflict accepts no messages.

` + sessionFields + `
Examples:
  gripi send 01a107aa "Run the tests and fix what fails"
  git diff | gripi send 01a107aa
  gripi send 01a107aa --steer "Stop and summarise what you have so far"

Exit codes:
` + sessionExitCodes

func sendMessage(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("send", flag.ContinueOnError)
	steer := flags.Bool("steer", false, "")
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "send", err.Error())
	}
	if len(positional) < 1 || len(positional) > 2 || positional[0] == "" {
		return usageError(stderr, "send", "takes a session and one message; quote a message that has spaces")
	}
	message := ""
	if len(positional) == 2 && positional[1] != "-" {
		message = positional[1]
	} else {
		// Without this, a forgotten message would leave the command waiting on a terminal forever.
		if file, ok := stdin.(*os.File); ok {
			if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
				return usageError(stderr, "send", "needs a message as an argument or on stdin")
			}
		}
		piped, err := io.ReadAll(stdin)
		if err != nil {
			return failure(stderr, "send", err)
		}
		message = string(piped)
	}
	if message = strings.TrimSpace(message); message == "" {
		return usageError(stderr, "send", "the message is empty")
	}

	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "send", err)
	}
	session, err := client.session(positional[0])
	if err != nil {
		return failure(stderr, "send", err)
	}
	behavior := "follow_up"
	if *steer {
		behavior = "steer"
	}
	// bash_mode keeps a leading "!" from being run as a shell command.
	form := url.Values{"session": {session.Path}, "message": {message}, "streaming_behavior": {behavior}, "bash_mode": {"prompt"}}
	request, err := http.NewRequest(http.MethodPost, "http://gripi/prompt", strings.NewReader(form.Encode()))
	if err != nil {
		return failure(stderr, "send", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var accepted struct {
		Session string `json:"session"`
	}
	if err := client.do(request, &accepted); err != nil {
		return failure(stderr, "send", err)
	}

	// Pi accepts a prompt just before it reports the turn. Returning in that gap
	// would let a following 'gripi wait' see an idle session and finish at once.
	// A message that starts no turn, such as an extension command, waits this out.
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		current, err := client.sessions(accepted.Session)
		if err != nil {
			return failure(stderr, "send", err)
		}
		if len(current) == 1 {
			session = current[0]
		}
		if session.State != "idle" || time.Now().After(deadline) {
			return printSession(stdout, stderr, "send", session, *asJSON)
		}
	}
}

// session finds the one session a path, an ID or an ID prefix refers to.
func (client *gatewayClient) session(reference string) (gateway.LocalSession, error) {
	sessions, err := client.sessions("")
	if err != nil {
		return gateway.LocalSession{}, err
	}
	var matches []gateway.LocalSession
	for _, session := range sessions {
		if session.Path == reference || session.ID == reference {
			return session, nil
		}
		if session.ID != "" && strings.HasPrefix(session.ID, reference) {
			matches = append(matches, session)
		}
	}
	switch len(matches) {
	case 0:
		return gateway.LocalSession{}, fmt.Errorf("no session matches %q; 'gripi list --all' shows every session", reference)
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for index, match := range matches {
		ids[index] = match.ID
	}
	return gateway.LocalSession{}, fmt.Errorf("%q matches %d sessions (%s); use more of the ID", reference, len(matches), strings.Join(ids, ", "))
}

func printSession(stdout, stderr io.Writer, name string, session gateway.LocalSession, asJSON bool) int {
	if asJSON {
		return printJSON(stdout, stderr, name, session)
	}
	printSessions(stdout, []gateway.LocalSession{session})
	return 0
}

const waitHelp = `Usage:
  gripi wait <session> [--timeout <seconds>] [--json]

Blocks until the session is no longer working or compacting, then prints it.
Run it after 'gripi send' to get the reply: last_reply has its first 180
characters, and the session file at path has the whole conversation.

Check the printed state. idle means Pi finished. waiting, external and
conflict also end the wait, because Pi will not continue until someone acts in
the browser or in Pi CLI.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --timeout <seconds>  Give up after this long; the session is still printed
  --json               Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi send 01a107aa "Run the tests" && gripi wait 01a107aa --timeout 900 --json

Exit codes:
` + sessionExitCodes + `  3  --timeout passed while the session was still working
`

func waitForSession(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("wait", flag.ContinueOnError)
	timeout := flags.Float64("timeout", 0, "")
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "wait", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "wait", "takes exactly one session")
	}
	if *timeout < 0 {
		return usageError(stderr, "wait", "--timeout cannot be negative")
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "wait", err)
	}
	session, err := client.session(positional[0])
	if err != nil {
		return failure(stderr, "wait", err)
	}
	deadline := time.Now().Add(time.Duration(*timeout * float64(time.Second)))
	for settled := false; ; {
		current, err := client.sessions(session.Path)
		if err != nil {
			return failure(stderr, "wait", err)
		}
		if len(current) != 1 {
			return failure(stderr, "wait", fmt.Errorf("session %s no longer exists", session.Path))
		}
		session = current[0]
		working := session.State == "working" || session.State == "compacting"
		// The gateway does not re-read a busy session's file, so the listing that
		// first shows a finished turn can still carry the reply before it.
		if !working && settled {
			return printSession(stdout, stderr, "wait", session, *asJSON)
		}
		if settled = !working; settled {
			continue
		}
		if *timeout > 0 && time.Now().After(deadline) {
			fmt.Fprintf(stderr, "gripi wait: still working after %g seconds\n", *timeout)
			printSession(stdout, stderr, "wait", session, *asJSON)
			return exitTimeout
		}
		time.Sleep(250 * time.Millisecond)
	}
}
