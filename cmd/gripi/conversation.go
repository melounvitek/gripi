package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	gateway "github.com/melounvitek/gripi/internal/server"
)

const showHelp = `Usage:
  gripi show <session> [--all | --full | --tail <count>] [--role <role>] [--json]

Prints Pi's reply to the latest message of a session, in full. Run it after
'gripi wait' to read what Pi answered. It prints nothing when there is no
such reply, for example because the turn ended in an error.

With --all it prints the recent conversation instead: each message under its
role, and each tool call on one line. A long conversation starts at a recent
message; --full prints all of it, and --tail as much as it takes for the last
messages. They follow the conversation's current branch, as the browser shows
it, without the branches that /tree left behind.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --all            Print the recent conversation instead of only the latest
                   reply
  --full           Print the whole conversation
  --tail <count>   Print the last messages of the conversation, this many
  --role <role>    Print only the messages with this role; repeat it or
                   separate roles with commas for several. Without --full
                   or --tail it filters the recent conversation.
  --json           Print the messages as a JSON array instead of text

Message fields:
  role       user       a message sent to Pi
             assistant  text that Pi replied with
             tool       a tool call; text has the tool's name or the shell
                        command, never the output
             status     a compaction of the context; text only says so
             error      an error that ended a turn
             custom     a message that an extension shows
  text       The whole message, except for tool and status
  timestamp  Time the message was written, in UTC with milliseconds, such
             as 2026-10-06T18:47:02.725Z
  images     Only in JSON, and only when the message has images, such as
             the screenshots of a tool call; 'gripi images' writes them
             to files

Examples:
  gripi send 01a107aa "Run the tests" && gripi wait 01a107aa && gripi show 01a107aa
  gripi show 01a107aa --tail 5           # what Pi is doing now
  gripi show 01a107aa --full --role user  # every message sent to Pi

Exit codes:
` + sessionExitCodes

var messageRoles = []string{"user", "assistant", "tool", "status", "error", "custom"}

func showConversation(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("show", flag.ContinueOnError)
	all := flags.Bool("all", false, "")
	full := flags.Bool("full", false, "")
	tail := flags.Int("tail", 0, "")
	asJSON := flags.Bool("json", false, "")
	var roles []string
	flags.Func("role", "", func(value string) error {
		for role := range strings.SplitSeq(value, ",") {
			if !slices.Contains(messageRoles, role) {
				return fmt.Errorf("%q is not a role; roles are %s", role, strings.Join(messageRoles, ", "))
			}
			roles = append(roles, role)
		}
		return nil
	})
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "show", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "show", "takes exactly one session")
	}
	if *tail < 0 {
		return usageError(stderr, "show", "--tail cannot be negative")
	}
	if *full && *tail > 0 {
		return usageError(stderr, "show", "--full and --tail cannot be combined")
	}
	conversationMode := *all || *full || *tail > 0 || len(roles) > 0
	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "show", err)
	}
	selected := func(messages []gateway.LocalMessage) []gateway.LocalMessage {
		return slices.DeleteFunc(slices.Clone(messages), func(message gateway.LocalMessage) bool {
			return len(roles) > 0 && !slices.Contains(roles, message.Role)
		})
	}
	conversation, err := client.conversation(session.Path, func(messages []gateway.LocalMessage) bool {
		return *full || len(selected(messages)) < *tail
	})
	if err != nil {
		return failure(stderr, "show", err)
	}
	messages := selected(conversation)
	if *tail > 0 && len(messages) > *tail {
		messages = messages[len(messages)-*tail:]
	}
	if !conversationMode {
		messages = []gateway.LocalMessage{}
		for _, message := range conversation {
			switch message.Role {
			// Neither a reply from before the latest message nor what Pi wrote before a turn failed is an answer.
			case "user", "error":
				messages = []gateway.LocalMessage{}
			case "assistant":
				messages = []gateway.LocalMessage{message}
			}
		}
	}
	if *asJSON {
		return printJSON(stdout, stderr, "show", messages)
	}
	if len(messages) == 0 && !conversationMode {
		fmt.Fprintln(stderr, "gripi show: there is no reply to the latest message; 'gripi show --all' prints what happened")
	}
	for index, message := range messages {
		switch {
		case !conversationMode:
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

// conversation reads a session's recent messages, then older ones for as long as more asks for them.
func (client *gatewayClient) conversation(path string, more func([]gateway.LocalMessage) bool) ([]gateway.LocalMessage, error) {
	var messages []gateway.LocalMessage
	leaf := ""
	for before := -1; ; {
		route := "/conversation?session=" + url.QueryEscape(path)
		if before >= 0 {
			// The same branch, even when /tree has moved to another one meanwhile.
			route += "&before=" + strconv.Itoa(before) + "&leaf=" + url.QueryEscape(leaf)
		}
		var page struct {
			Messages []gateway.LocalMessage `json:"messages"`
			Start    int                    `json:"start"`
			Leaf     string                 `json:"leaf"`
		}
		if err := client.get(route, &page); err != nil {
			return nil, err
		}
		if before >= 0 && page.Start >= before {
			return nil, errors.New("the conversation changed while it was read; run the command again")
		}
		// Each page ends where the one after it starts, so nothing is missed or listed twice.
		messages = append(page.Messages, messages...)
		if page.Start == 0 || !more(messages) {
			return messages, nil
		}
		if before < 0 {
			leaf = page.Leaf
		}
		before = page.Start
	}
}

const imagesHelp = `Usage:
  gripi images <session> --out <directory> [--json]

Writes the images stored in a session to files, such as the screenshots that
Pi's tools took and the images sent to Pi, from the whole conversation. Then
it prints the path of each file, oldest first.

A file is named after the time of the message that holds the image, so the
names sort in the conversation's order, such as
2026-10-06T18-47-02.725Z-5f2a9c1e-0.png. A file that is already there is
replaced, so running the command again writes the same files.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Flags:
  --out <directory>  Directory for the files; it is created when missing
  --json             Print a JSON array instead, with the fields path, role,
                     timestamp and mime_type of each file

Example:
  gripi images 01a107aa --out /tmp/screenshots

Exit codes:
` + sessionExitCodes

// writtenImage is how gripi images reports a file that it wrote.
type writtenImage struct {
	Path      string            `json:"path"`
	Role      string            `json:"role"`
	Timestamp gateway.Timestamp `json:"timestamp"`
	MIMEType  string            `json:"mime_type"`
}

func saveImages(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("images", flag.ContinueOnError)
	out := flags.String("out", "", "")
	asJSON := flags.Bool("json", false, "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "images", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "images", "takes exactly one session")
	}
	if *out == "" {
		return usageError(stderr, "images", "needs --out with a directory for the files")
	}
	client, session, err := connect(positional[0])
	if err != nil {
		return failure(stderr, "images", err)
	}
	messages, err := client.conversation(session.Path, func([]gateway.LocalMessage) bool { return true })
	if err != nil {
		return failure(stderr, "images", err)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return failure(stderr, "images", err)
	}
	written := []writtenImage{}
	for _, message := range messages {
		for _, image := range message.Images {
			var data []byte
			route := fmt.Sprintf("/session_images/%s/%d?session=%s", url.PathEscape(image.EntryID), image.Index, url.QueryEscape(session.Path))
			if err := client.get(route, &data); err != nil {
				return failure(stderr, "images", err)
			}
			extension := strings.TrimPrefix(image.MIMEType, "image/")
			if extension == "jpeg" {
				extension = "jpg"
			}
			// Colons would not do in file names on every system.
			name := fmt.Sprintf("%s-%s-%d.%s", message.Timestamp.UTC().Format("2006-01-02T15-04-05.000Z"), image.EntryID, image.Index, extension)
			path := filepath.Join(*out, filepath.Base(name))
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return failure(stderr, "images", err)
			}
			written = append(written, writtenImage{Path: path, Role: message.Role, Timestamp: message.Timestamp, MIMEType: image.MIMEType})
		}
	}
	if *asJSON {
		return printJSON(stdout, stderr, "images", written)
	}
	if len(written) == 0 {
		fmt.Fprintln(stderr, "gripi images: the session has no images")
	}
	for _, image := range written {
		fmt.Fprintln(stdout, image.Path)
	}
	return 0
}

const openHelp = `Usage:
  gripi open <session>

Continues a session in Pi CLI. It changes to the session's project directory
and replaces itself with 'pi --session <path>', so it needs a terminal and
does not return until Pi CLI exits.

The gateway must not be running the session: its state has to be idle,
external or conflict, and Pi must have replied in it at least once. Once Pi
CLI has written to the session, the gateway only follows it (state external)
until someone takes it over in the browser.

Pi CLI starts as it does for any session. It does not get the environment
variables saved in Gripi, and it asks before it trusts a project.

Arguments:
  session  Session ID, a unique prefix of it, or the session file path

Example:
  gripi open 01a107aa

Exit codes:
  Pi CLI's own once it has started. Before that:
  1  the gateway cannot be reached or did not answer, no single session
     matched, the gateway is running the session or it has no reply yet,
     there is no terminal, or pi is not on PATH
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
	if session.Agent != "" && session.Agent != "pi" {
		return failure(stderr, "open", fmt.Errorf("gripi open continues Pi sessions only, and this session runs with %s", session.Agent))
	}
	_, err = os.Stat(session.Path)
	switch {
	// Until the first reply only the gateway's Pi process has the session; it has no file yet.
	case errors.Is(err, os.ErrNotExist):
		return failure(stderr, "open", errors.New("the session has no reply yet, so Pi CLI has nothing to continue"))
	case session.State != "idle" && session.State != "external" && session.State != "conflict":
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
