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
	"github.com/melounvitek/gripi/internal/prompts"
	gateway "github.com/melounvitek/gripi/internal/server"
)

const listLimit = 20

const sessionFields = `Session fields:
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
  pinned      true when the session is pinned in the browser's sidebar
  tags        Tags of the session
  updated_at  Time of the latest message; in JSON it is in UTC with
              milliseconds, such as 2026-10-06T18:47:02.725Z
  last_reply  First 180 characters of the latest assistant reply
`

const sessionExitCodes = `  0  success
  1  the gateway cannot be reached or did not answer, it refused the request,
     or no single session matched
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

// Tests shorten these.
var (
	readTimeout = 30 * time.Second
	// The gateway gives Pi 30 seconds for each of the few requests that an action takes, and answers
	// when one runs out. Giving up before that could report a delivered message as not sent.
	actionTimeout = 5 * time.Minute
	retryWindow   = 5 * time.Second
	// The gateway gives Pi 10 seconds to stop before it ends the process.
	stopWindow = 15 * time.Second
)

// momentaryRefusal is an answer that the gateway would not give a moment later.
// It had not begun the request, so asking again repeats nothing.
type momentaryRefusal struct{ error }

// gatewayClient reaches the running gateway through its private socket.
type gatewayClient struct {
	http *http.Client
	// deadline, when set, shortens reads and ends settled.
	deadline time.Time
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

func (client *gatewayClient) do(request *http.Request, timeout time.Duration, result any) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request = request.WithContext(ctx)
	request.Header.Set("Accept", "application/json")
	response, err := client.http.Do(request)
	var body []byte
	if err == nil {
		defer response.Body.Close()
		body, err = io.ReadAll(response.Body)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the gateway did not answer within %s", timeout.Round(time.Second))
	}
	if err != nil {
		var wrapped *url.Error
		if errors.As(err, &wrapped) {
			err = wrapped.Err
		}
		return fmt.Errorf("cannot reach the gateway (%w). Is it running? A gateway in multi-user mode accepts no commands", err)
	}
	// The gateway answers 202 to a stop that it has not finished yet.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		var failure struct {
			Error     string `json:"error"`
			Code      string `json:"code"`
			Retryable *bool  `json:"retryable"`
		}
		if json.Unmarshal(body, &failure) != nil || failure.Error == "" {
			failure.Error = strings.TrimSpace(string(body))
		}
		err := fmt.Errorf("the gateway answered %d: %s", response.StatusCode, failure.Error)
		// Another request for the session is being handled, or its Pi process is being replaced.
		// retryable is false only while the session tree changes, after which the message may not fit.
		pending := failure.Code == "session_operation_pending" && failure.Retryable == nil
		if pending || response.Header.Get("Retry-After") != "" {
			return momentaryRefusal{err}
		}
		return err
	}
	return json.Unmarshal(body, result)
}

func (client *gatewayClient) get(route string, result any) error {
	request, err := http.NewRequest(http.MethodGet, "http://gripi"+route, nil)
	if err != nil {
		return err
	}
	timeout := readTimeout
	if !client.deadline.IsZero() {
		// Even a poll at the deadline gets two seconds, so that a busy gateway is not taken for a silent one.
		timeout = min(timeout, max(time.Until(client.deadline), 2*time.Second))
	}
	return client.do(request, timeout, result)
}

func (client *gatewayClient) post(route string, form url.Values, result any) error {
	request, err := http.NewRequest(http.MethodPost, "http://gripi"+route, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return client.do(request, actionTimeout, result)
}

// sessions lists every session, or just the one with the given path.
func (client *gatewayClient) sessions(only string) ([]gateway.LocalSession, error) {
	route := "/sessions"
	if only != "" {
		route += "?session=" + url.QueryEscape(only)
	}
	var payload struct {
		Sessions []gateway.LocalSession `json:"sessions"`
	}
	return payload.Sessions, client.get(route, &payload)
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
	total := len(sessions)
	if !*all && total > listLimit {
		sessions = sessions[:listLimit]
	}
	if *asJSON {
		return printJSON(stdout, stderr, "list", sessions)
	}
	printSessions(stdout, sessions)
	if len(sessions) < total {
		fmt.Fprintf(stderr, "Showing the latest %d of %d sessions; pass --all for the rest.\n", listLimit, total)
	}
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
	fmt.Fprintln(table, "ID\tSTATE\tUNREAD\tPINNED\tUPDATED\tPROJECT\tTAGS\tNAME")
	for _, session := range sessions {
		tags := "-"
		if len(session.Tags) > 0 {
			tags = strings.Join(session.Tags, ",")
		}
		name := []rune(strings.Join(strings.Fields(session.Name), " "))
		if len(name) > 60 {
			name = append(name[:59], '…')
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", session.ID, session.State, yesOrDash(session.Unread), yesOrDash(session.Pinned), age(time.Since(session.UpdatedAt.Time)), filepath.Base(session.CWD), tags, string(name))
	}
	table.Flush()
}

func yesOrDash(value bool) string {
	if value {
		return "yes"
	}
	return "-"
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

const newHelp = `Usage:
  gripi new <directory> [--json]

Starts a session in a project directory, then prints it. The session is empty
until 'gripi send' gives it a first message, and it keeps a Pi process running
until then, so start one only to use it.

Arguments:
  directory  Project directory for the session; it must exist

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  session=$(gripi new ~/Work/project --json | jq -r .id)
  gripi send "$session" "Run the tests" && gripi wait "$session"

Exit codes:
` + sessionExitCodes

func newSession(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("new", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "new", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "new", "takes exactly one directory")
	}
	// The gateway would resolve a relative directory against its own.
	directory, err := filepath.Abs(positional[0])
	if err != nil {
		return failure(stderr, "new", err)
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "new", err)
	}
	var started struct {
		Session string `json:"session"`
	}
	if err := client.post("/sessions/new_at_cwd", url.Values{"cwd": {directory}}, &started); err != nil {
		return failure(stderr, "new", err)
	}
	sessions, err := client.sessions(started.Session)
	if err != nil {
		return failure(stderr, "new", err)
	}
	if len(sessions) != 1 {
		return failure(stderr, "new", fmt.Errorf("session %s no longer exists", started.Session))
	}
	return printSession(stdout, stderr, "new", sessions[0], *asJSON)
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
current step and before Pi's next request to the model.

The text only ever reaches Pi as a prompt. A leading "!" does not run a shell
command, and the composer's own commands (/new, /compact, /name, /model,
/fork, /tree, /clone, /reload, /login, /logout) are refused rather than run.
Pi still expands its skills, prompt templates and extension commands, such as
/skill:name.

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
	// Steered, the gateway would run these as the composer does; otherwise Pi would get them as text.
	if prompts.ParseSlashCommand(message).Type != "" {
		return usageError(stderr, "send", strings.Fields(message)[0]+" is a command of the browser's composer, which send does not run")
	}

	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "send", err)
	}
	behavior := "follow_up"
	if *steer {
		behavior = "steer"
	}
	var accepted struct {
		Session     string `json:"session"`
		Disposition string `json:"disposition"`
	}
	// bash_mode keeps a leading "!" from being run as a shell command.
	form := url.Values{"session": {session.Path}, "message": {message}, "streaming_behavior": {behavior}, "bash_mode": {"prompt"}}
	for deadline := time.Now().Add(retryWindow); ; time.Sleep(100 * time.Millisecond) {
		err = client.post("/prompt", form, &accepted)
		if !errors.As(err, new(momentaryRefusal)) || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		return failure(stderr, "send", err)
	}

	// Pi accepts a prompt just before it reports the turn. Returning in that gap
	// would let a following 'gripi wait' see an idle session and finish at once.
	// No turn follows a message that Pi handled itself, such as an extension command.
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		current, err := client.sessions(accepted.Session)
		if err != nil {
			// The message is delivered, so this must not look like a failed send.
			fmt.Fprintf(stderr, "gripi send: delivered, but the session could not be read back: %v\n", err)
		} else if len(current) == 1 {
			session = current[0]
		}
		if err != nil || session.State != "idle" || accepted.Disposition == "handled" || time.Now().After(deadline) {
			return printSession(stdout, stderr, "send", session, *asJSON)
		}
	}
}

// connect reaches the gateway and finds the session that the reference names.
func connect(reference string) (*gatewayClient, gateway.LocalSession, error) {
	client, err := newGatewayClient()
	if err != nil {
		return nil, gateway.LocalSession{}, err
	}
	session, err := client.session(reference)
	return client, session, err
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
		if strings.HasPrefix(session.ID, reference) {
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
Without --timeout it waits as long as that takes. Run it after 'gripi send',
then read the reply with 'gripi show'. last_reply has the first 180 characters
of the session's latest reply, which is an older one when the turn failed.

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
	if *timeout > 0 {
		client.deadline = time.Now().Add(time.Duration(*timeout * float64(time.Second)))
	}
	session, err := client.session(positional[0])
	if err != nil {
		return failure(stderr, "wait", err)
	}
	session, settled, err := client.settled(session)
	if err != nil {
		return failure(stderr, "wait", err)
	}
	if !settled {
		fmt.Fprintf(stderr, "gripi wait: still working after %g seconds\n", *timeout)
		printSession(stdout, stderr, "wait", session, *asJSON)
		return exitTimeout
	}
	return printSession(stdout, stderr, "wait", session, *asJSON)
}

// settled polls the session until it is neither working nor compacting.
// It reports false when the client's deadline passes first.
func (client *gatewayClient) settled(session gateway.LocalSession) (gateway.LocalSession, bool, error) {
	for settled := false; ; {
		current, err := client.sessions(session.Path)
		if err != nil {
			return session, false, err
		}
		if len(current) != 1 {
			return session, false, fmt.Errorf("session %s no longer exists", session.Path)
		}
		session = current[0]
		working := session.State == "working" || session.State == "compacting"
		// The gateway does not re-read a busy session's file, so the listing that
		// first shows a finished turn can still carry the reply before it.
		if !working && settled {
			return session, true, nil
		}
		if settled = !working; settled {
			continue
		}
		if !client.deadline.IsZero() && time.Now().After(client.deadline) {
			return session, false, nil
		}
		time.Sleep(time.Second)
	}
}

const stopHelp = `Usage:
  gripi stop <session> [--json]

Stops what the gateway is running in a session, as the Stop button in the
browser does, then prints the session. It ends the turn or the compaction
where it is. An idle session is left as it is.

Messages queued behind the stopped turn are not sent to Pi. The command prints
them on stderr instead, as the browser puts them back in the composer. Only in
a session that has no reply yet do they stay queued, and Pi takes them up in
its next turn.

A shell command that someone started in the browser's composer is stopped
before anything else. A turn that was running beside it goes on, so the
command fails with the session still working; run it again to stop the turn.

A question that Pi asked in the browser can stay open after the stop, and the
session then reads as waiting until someone answers there. That state does
not tell whether the turn has ended.

The gateway cannot stop Pi CLI, so the command fails for a session in state
external or conflict.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi stop 01a107aa && gripi send 01a107aa "Try the other approach"

Exit codes:
  0  the session is no longer working or compacting
  1  the gateway cannot be reached or did not answer, it refused the request,
     no single session matched, or the session is still working
  2  usage error
`

func stopSession(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("stop", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "stop", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "stop", "takes exactly one session")
	}
	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "stop", err)
	}
	// The gateway would start Pi for an idle session only to tell it to stop.
	if session.State != "idle" {
		var stop struct {
			EditorText string `json:"editorText"`
		}
		if err := client.post("/abort", url.Values{"session": {session.Path}}, &stop); err != nil {
			return failure(stderr, "stop", err)
		}
		if stop.EditorText != "" {
			fmt.Fprintf(stderr, "gripi stop: these messages were queued behind the stopped turn and were not sent:\n%s\n", stop.EditorText)
		}
		// The gateway may answer while it is still stopping the session.
		client.deadline = time.Now().Add(stopWindow)
		var stopped bool
		if session, stopped, err = client.settled(session); err != nil {
			return failure(stderr, "stop", err)
		}
		if !stopped {
			return failure(stderr, "stop", fmt.Errorf("the session is still %s. If a shell command was running in it, only that was stopped; run 'gripi stop' again", session.State))
		}
	}
	return printSession(stdout, stderr, "stop", session, *asJSON)
}

const deleteHelp = `Usage:
  gripi delete <session> [--json]

Deletes a session, then prints it as it was. The command does not ask first.

The session file moves to Trash when the 'trash' or 'gio' command can do that
on the gateway's machine; otherwise it is deleted for good. The session's pin,
its tags and the files attached to its messages are always deleted for good.

The gateway refuses while Pi is running a turn in the session or compacting
it; wait for it with 'gripi wait' or end it with 'gripi stop' first. A turn
that asked a question (state waiting) keeps running until someone answers in
the browser or stops it. The gateway does not know whether Pi CLI still has
the session open, so a session in state external or conflict is deleted like
any other.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --json  Print the session as a JSON object instead of a table

` + sessionFields + `
Example:
  gripi delete 01a107aa

Exit codes:
` + sessionExitCodes

func deleteSession(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("delete", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "delete", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "delete", "takes exactly one session")
	}
	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "delete", err)
	}
	if err := client.post("/sessions/delete", url.Values{"session": {session.Path}}, &struct{}{}); err != nil {
		return failure(stderr, "delete", err)
	}
	return printSession(stdout, stderr, "delete", session, *asJSON)
}
