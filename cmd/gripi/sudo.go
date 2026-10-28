package main

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const sudoHelp = `Usage:
  gripi sudo -- <command> [arguments]

Asks the user to run a command as root. Gripi shows the request in the
conversation of the Pi or Hermes session that runs gripi sudo, and waits up
to 2 minutes for the user's answer there.

Run it from a session in Gripi, which it finds by PI_SESSION_FILE, set by
Pi's bash tool, or by HERMES_SESSION_ID, set by Hermes's terminal.

Example:
  gripi sudo -- apt-get install -y ripgrep

Exit codes:
  1  the gateway cannot be reached, the command does not run in a session of
     yours in Gripi, the user cancelled, or nobody answered in time
  2  usage error
`

func runAsRoot(arguments []string, _ io.Reader, _, stderr io.Writer) int {
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
	// The gateway answers once the user has, or once the request expires, and its socket ends every request by serverReadTimeout.
	if err := client.do(request, serverReadTimeout, &struct{}{}); err != nil {
		return failure(stderr, "sudo", err)
	}
	return 0
}
