package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	gateway "github.com/melounvitek/gripi/internal/server"
)

const sudoHelp = `Usage:
  gripi sudo -- <command> [arguments]

Asks the user to run a command as root and waits up to 2 minutes for them to
answer in Gripi, where the request takes the editor's place in the
conversation of the Pi or Hermes session that runs gripi sudo. Once they enter
their password, Gripi runs the command with sudo and passes on its output and
exit code. The password goes only to sudo, never to the agent.

Each command needs the user's password again. The command reads no input, and
Gripi keeps only the first 16 MiB of each of its outputs.

gripi sudo finds its session by PI_SESSION_FILE, which Pi's bash tool sets, or
by HERMES_SESSION_ID, which Hermes's terminal sets.

Example:
  gripi sudo -- apt-get install -y ripgrep

Exit codes:
  Once the user has entered the password, sudo's: the command's own, or 1
  when sudo fails, as after a wrong password. Otherwise gripi sudo prints why,
  starting with "gripi sudo:", and exits with
  1  the gateway cannot be reached, the command does not run in a session of
     yours in Gripi, the user cancelled, or nobody answered in 2 minutes
  2  usage error
`

func runAsRoot(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) < 2 || arguments[0] != "--" {
		return usageError(stderr, "sudo", "put the command after --, as in 'gripi sudo -- apt-get update'")
	}
	session := os.Getenv("PI_SESSION_FILE")
	if id := os.Getenv("HERMES_SESSION_ID"); session == "" && id != "" {
		session = "hermes:" + id
	}
	if session == "" {
		return failure(stderr, "sudo", errors.New("run it from a Pi or Hermes session in Gripi; neither PI_SESSION_FILE nor HERMES_SESSION_ID is set"))
	}
	cwd, err := os.Getwd()
	if err != nil {
		return failure(stderr, "sudo", err)
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "sudo", err)
	}
	form := url.Values{"session": {session}, "cwd": {cwd}, "argv": arguments[1:]}
	request, err := http.NewRequest(http.MethodPost, "http://gripi/sudo", strings.NewReader(form.Encode()))
	if err != nil {
		return failure(stderr, "sudo", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The gateway answers once the command has run, however long that takes. Its socket's read timeout stops
	// counting once the gateway has read the request.
	var result gateway.SudoResult
	if err := client.do(request, 0, &result); err != nil {
		return failure(stderr, "sudo", err)
	}
	stdout.Write(result.Stdout)
	stderr.Write(result.Stderr)
	if result.Truncated {
		fmt.Fprintln(stderr, "gripi sudo: the command printed more than 16 MiB to an output, and Gripi dropped the rest")
	}
	return result.ExitCode
}

// answerSudoPrompt is gripi as the askpass program of a sudo that the gateway runs. It prints the user's password
// only for sudo's password prompt, once, and otherwise prints nothing at all, after which sudo gives up.
func answerSudoPrompt(arguments []string, stdout, stderr io.Writer) int {
	// Where sudoers keeps the environment, a command that sudo runs has the token too, and may run gripi.
	parent, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", os.Getppid()))
	if len(arguments) != 1 || arguments[0] != gateway.SudoAskpassPrompt || err != nil || string(parent) != "sudo\n" {
		fmt.Fprintln(stderr, "gripi sudo: Gripi answers only the password prompt of the sudo that it runs")
		return exitFailure
	}
	client, err := newGatewayClient()
	if err != nil {
		return failure(stderr, "sudo", err)
	}
	response, err := client.http.PostForm("http://gripi/sudo/askpass", url.Values{"token": {os.Getenv("GRIPI_SUDO_TOKEN")}})
	if err != nil {
		return failure(stderr, "sudo", err)
	}
	defer response.Body.Close()
	password, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "gripi sudo: Gripi gives sudo the password only once")
		return exitFailure
	}
	stdout.Write(append(password, '\n'))
	return 0
}
