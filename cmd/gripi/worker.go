package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	"github.com/melounvitek/gripi/internal/state"
	remote "github.com/melounvitek/gripi/internal/worker"
	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
)

const workerHelp = `Usage:
  gripi worker add <user@host> [--name <name>]
  gripi worker list [--json]
  gripi worker remove <name>
  gripi worker move <session> <name>
  gripi worker return <session>
  gripi worker forward <session> <port>
  gripi worker unforward <session> <port>

Internal, run by Gripi on a worker over SSH:
  gripi worker serve [--lease <duration>] [--runtime-dir <folder>] [--home <folder>] [--data-dir <folder>]
  gripi worker attach [--runtime-dir <folder>] <session> <helper instance>
  gripi worker receive [--runtime-dir <folder>] <folder on main> <folder> <helper instance>
  gripi worker receive [--runtime-dir <folder>] --move <move> <folder on main> <helper instance>
  gripi worker receive [--runtime-dir <folder>] --file <file> <helper instance>
  gripi worker mirror [--runtime-dir <folder>] --move <move> [--file <file>] <helper instance>
  gripi worker enter --home <folder> -- <command>
  gripi worker sudo [--runtime-dir <folder>]

A worker is another computer that runs sessions for this gateway. Gripi
reaches it with this machine's ssh, as the gateway's user and with that
user's SSH configuration. Workers belong to the whole gateway. add, list and
remove need no running gateway; move, return, forward and unforward do.

add connects to the worker without asking for anything, so ssh must already
trust the worker's host key and log in with a key. It checks that the worker
has Git, the same operating system and architecture as this machine, and a
systemd user manager with cgroup v2, which lets Gripi stop everything a
session started.
Sessions use this machine's paths on the worker. The worker's user can have
any name and home folder: when the home folder has another path, what Gripi
runs there sees it at this machine's home folder, in a private view. Each
moved session runs in a private view of its copy too. The views need
unprivileged user namespaces, which add checks with Gripi's helper there.
add fails when the worker's user cannot create Pi's folder
(~/.pi/agent) at its path here, or when the worker shares this machine's
files, as this machine itself as the same user or a home folder mounted on
both would. When the worker's user can't use the worker's Docker, add says
why and what an admin of the worker can do. The worker will hold a full copy
of Pi's configuration, including logins; keeping it secure is up to you.

list prints each worker's name and SSH destination.

remove forgets a worker, unless sessions are on it. Files on the worker stay
there.

move runs one session on a worker from now on, on a private copy of its
repository there: the repository's Git folder, the worktree that the session
started in and those that its tool calls worked in. The repository must be
in your home folder. Its other sessions keep running on this machine. move
needs a running gateway, which waits until those sessions have finished
their steps, holding their new prompts meanwhile, copies the repository and
the session to the worker, and keeps copies of both here up to date. move
fails while the session itself is working. return brings the session back
with what it changed there: what only the session changed comes back,
changes to different lines of a text file are merged, and what this machine
changed too stays as this machine has it, with the worker's version kept in
Gripi's state folder, which the session's page names. When this machine has
lost the worker, return waits until the worker must have stopped the
session, and says when that is; then the session continues here with the
changes that this machine last got from the worker.

forward makes a port on the worker that runs the session available on this
machine, such as a dev server that the session started there: at the same
port on localhost, or at the next free one, which it prints. Both ends
listen only on localhost. The forward lasts until unforward, or until this
machine's connection to the worker ends.

serve is the helper that runs the worker's sessions, and stops all of them
when this machine stops answering. With --home, it runs in the private view
of that folder, as enter does. attach connects to the Pi of a session.
receive connects this machine's copy of a folder to the helper, which makes
a folder a copy of it, a move's copy of this machine's shadow of the
repository, or a copy of a session's file. mirror connects this machine's
copy of a move's repository, or of its session's file, to the helper, which
sends their changes. enter runs a command in a private view in which the
folder of --home, this machine's home folder, shows the worker user's own
home folder; when that is the same folder, the view changes nothing, but
enter still makes one, as moved sessions need. sudo runs, outside the views,
the sudo of a command that gripi sudo asked for in a session there, as its
input says.

Gripi keeps the workers in ~/.pi/gripi/workers.json, or in the file named by
GRIPI_WORKERS_PATH, and which sessions are on which worker in
~/.pi/gripi/worker-placements.json, or in GRIPI_WORKER_PLACEMENTS_PATH.

Arguments:
  user@host  Where ssh connects, as you would pass it to ssh; a Host from
             your SSH configuration works too
  name       Name of a worker
  session    Session ID, a unique prefix of it, or the session file path
  port       A port on the worker's localhost

Flags:
  --name  Name of the new worker; defaults to the first part of the host name,
          such as beelink for gripi@beelink.example.ts.net
  --json  Print a JSON array of {"name", "destination"} objects instead of a
          table

Examples:
  gripi worker add gripi@beelink.example.ts.net
  gripi worker move 01a107aa beelink
  gripi worker return 01a107aa
  gripi worker forward 01a107aa 5173
  gripi worker remove beelink

Exit codes:
  0  success
  1  the worker cannot be reached or cannot run sessions, the name is taken,
     no worker has the name, sessions are on the worker that remove names,
     or the gateway refused or failed to move the session or to forward the
     port
  2  usage error
`

type worker struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

// workerProbe prints the worker's system, architecture, user and home, then "git" if it has Git, "systemd" if
// systemd-run can start a command under the user's systemd with cgroup v2, and if it has the docker CLI, "docker: "
// and the last line of `docker version` under the user's systemd, where the helper runs and the groups of a new login
// may be missing: "available" or why Docker can't be reached.
// ssh runs it in the worker user's shell, so it uses only what both sh and fish accept.
const workerProbe = `uname -s; uname -m; id -un; echo "$HOME"; command -v git >/dev/null && echo git; ` +
	`systemd-run --user --pipe --quiet test -f /sys/fs/cgroup/cgroup.controllers && echo systemd; ` +
	`systemd-run --user --pipe --quiet sh -c 'command -v docker > /dev/null && echo "docker: $(timeout 10 docker version --format available 2>&1 | tail -n 1)"'`

// workerHelper is where `gripi worker add` installs the helper on a worker to check the private views there: this
// executable, in remote.DefaultInstallDir, unless a test changes it.
var workerHelper remote.Options

// viewScripts installs the helper on a worker and returns what runs sh scripts in a private view there, unless a test
// changes it.
var viewScripts = remote.ViewScripts

func manageWorkers(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) == 0 {
		return usageError(stderr, "worker", "needs add, list, remove, move, return, forward or unforward")
	}
	switch arguments[0] {
	case "add":
		return addWorker(arguments[1:], stdout, stderr)
	case "list":
		return listWorkers(arguments[1:], stdout, stderr)
	case "remove":
		return removeWorker(arguments[1:], stdout, stderr)
	case "move":
		return moveSession(arguments[1:], stdout, stderr)
	case "return":
		return returnSession(arguments[1:], stdout, stderr)
	case "forward", "unforward":
		return forwardPort(arguments[0], arguments[1:], stdout, stderr)
	case "serve":
		return serveWorker(arguments[1:], stdin, stdout, stderr)
	case "attach":
		return attachWorker(arguments[1:], stdin, stdout, stderr)
	case "receive":
		return receiveWorker(arguments[1:], stdin, stdout, stderr)
	case "mirror":
		return mirrorWorker(arguments[1:], stdin, stdout, stderr)
	case "move-host":
		return hostMove(arguments[1:], stdin, stdout, stderr)
	case "enter":
		return enterWorker(arguments[1:], stdin, stdout, stderr)
	case "sudo":
		return sudoWorker(arguments[1:], stdin, stdout, stderr)
	}
	return usageError(stderr, "worker", fmt.Sprintf("unknown subcommand %q; use add, list, remove, move, return, forward or unforward", arguments[0]))
}

func addWorker(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker add", flag.ContinueOnError)
	name := flags.String("name", "", "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	if len(positional) != 1 || positional[0] == "" {
		return usageError(stderr, "worker", "add takes one user@host")
	}
	destination := positional[0]
	if *name == "" {
		*name, _, _ = strings.Cut(destination[strings.LastIndex(destination, "@")+1:], ".")
	}
	cfg, workers, err := loadWorkers()
	if err != nil {
		return failure(stderr, "worker", err)
	}
	if slices.ContainsFunc(workers, func(worker worker) bool { return worker.Name == *name }) {
		return failure(stderr, "worker", fmt.Errorf("a worker named %q exists already; choose another name with --name", *name))
	}

	lines, err := probeWorker(destination, workerProbe)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	architecture := lines[1]
	switch architecture {
	case "x86_64":
		architecture = "amd64"
	case "aarch64":
		architecture = "arm64"
	}
	if system, local := strings.ToLower(lines[0])+"/"+architecture, runtime.GOOS+"/"+runtime.GOARCH; system != local {
		return failure(stderr, "worker", fmt.Errorf("%s runs %s, but this machine runs %s; a worker needs the same operating system and architecture", destination, system, local))
	}
	if !slices.Contains(lines[4:], "git") {
		return failure(stderr, "worker", fmt.Errorf("%s has no Git; install it there, then try again", destination))
	}
	if !slices.Contains(lines[4:], "systemd") {
		return failure(stderr, "worker", fmt.Errorf("%s cannot start commands with 'systemd-run --user' under cgroup v2; Gripi needs that to stop everything a session started", destination))
	}
	user, home := lines[2], lines[3]
	// Each moved session runs in a private view of its copy there, and with another home folder, everything that uses
	// this machine's paths runs in one that shows it at this machine's home folder.
	options := workerHelper
	options.Destination, options.Home = destination, cfg.Home
	run, err := viewScripts(context.Background(), options)
	if err != nil {
		return failure(stderr, "worker", fmt.Errorf("install Gripi's helper on %s: %w", destination, err))
	}
	if _, err := run("true"); err != nil {
		return failure(stderr, "worker", fmt.Errorf("%s on %s, whose home folder is %s, cannot run Gripi's sessions in private views: %w", user, destination, home, err))
	}
	if _, err := run(`mkdir -p "$1" && test -w "$1"`, cfg.PiAgentDir); err != nil {
		return failure(stderr, "worker", fmt.Errorf("%s cannot create Pi's folder %s on %s, or cannot write to it: %w", user, cfg.PiAgentDir, destination, err))
	}
	if err := remote.CheckSeparate(destination, run, cfg.PiAgentDir, filepath.Dir(cfg.SocketPath)); err != nil {
		return failure(stderr, "worker", err)
	}

	if err := saveWorkers(cfg.WorkersPath, append(workers, worker{Name: *name, Destination: destination})); err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, `Added worker %s (%s).
The worker will hold a full copy of Pi's configuration, including logins.
Keeping it secure is up to you.
`, *name, destination)
	for _, line := range lines[4:] {
		if reason, found := strings.CutPrefix(line, "docker: "); found && reason != "available" {
			fmt.Fprintf(stdout, "Docker: %s\n", dockerproxy.Unusable(*name, user, errors.New(reason)))
		}
	}
	return 0
}

// probeWorker runs probe on the worker and returns the lines it printed.
func probeWorker(destination, probe string) ([]string, error) {
	command := exec.Command("ssh", "-o", "BatchMode=yes", "--", destination, probe)
	var errorOutput strings.Builder
	command.Stderr = &errorOutput
	output, err := command.Output()
	var exit *exec.ExitError
	// ssh exits with 255 when it fails, and otherwise with the probe's status, which is 1 when a check fails.
	if errors.As(err, &exit) && exit.ExitCode() == 255 {
		sshError := strings.TrimSpace(errorOutput.String())
		switch {
		case strings.Contains(sshError, "Host key verification failed"):
			return nil, fmt.Errorf("ssh does not trust the host key of %s. Run 'ssh %s' once to check and accept it, then try again", destination, destination)
		case strings.Contains(sshError, "Permission denied"):
			return nil, fmt.Errorf("ssh cannot log in to %s with a key. Run 'ssh-copy-id %s' to set one up, then try again", destination, destination)
		}
		return nil, fmt.Errorf("ssh %s failed: %s", destination, cmp.Or(sshError, exit.Error()))
	}
	if err != nil && exit == nil {
		return nil, fmt.Errorf("cannot run ssh: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 4 {
		return nil, fmt.Errorf("unexpected answer from %s: %q %s", destination, output, strings.TrimSpace(errorOutput.String()))
	}
	return lines, nil
}

func listWorkers(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker list", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "")
	if positional, err := parseArguments(flags, arguments); err != nil {
		return usageError(stderr, "worker", err.Error())
	} else if len(positional) > 0 {
		return usageError(stderr, "worker", "list takes no arguments besides --json")
	}
	_, workers, err := loadWorkers()
	if err != nil {
		return failure(stderr, "worker", err)
	}
	if *asJSON {
		return printJSON(stdout, stderr, "worker", workers)
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tDESTINATION")
	for _, worker := range workers {
		fmt.Fprintf(table, "%s\t%s\n", worker.Name, worker.Destination)
	}
	table.Flush()
	return 0
}

func removeWorker(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 1 || arguments[0] == "" {
		return usageError(stderr, "worker", "remove takes one worker name")
	}
	name := arguments[0]
	cfg, workers, err := loadWorkers()
	if err != nil {
		return failure(stderr, "worker", err)
	}
	index := slices.IndexFunc(workers, func(worker worker) bool { return worker.Name == name })
	if index < 0 {
		return failure(stderr, "worker", fmt.Errorf("no worker is named %q", name))
	}
	placements, err := remote.LoadPlacements(cfg.WorkerPlacementsPath)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	for _, placement := range placements.All() {
		if placement.Worker == name {
			return failure(stderr, "worker", fmt.Errorf("session %s is on %s; bring it back with 'gripi worker return %s' first", placement.Session, name, placement.Session))
		}
	}
	if err := saveWorkers(cfg.WorkersPath, slices.Delete(workers, index, index+1)); err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "Removed worker %s. Files on the worker stay there.\n", name)
	return 0
}

func moveSession(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 2 || arguments[0] == "" || arguments[1] == "" {
		return usageError(stderr, "worker", "move takes a session and a worker name")
	}
	session, err := askAboutSession("/workers/move", url.Values{"worker": {arguments[1]}}, arguments[0], &struct{}{})
	if err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "Session %s runs on %s now, on a copy of its repository.\n", session.ID, arguments[1])
	return 0
}

func returnSession(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 1 || arguments[0] == "" {
		return usageError(stderr, "worker", "return takes a session")
	}
	session, err := askAboutSession("/workers/return", url.Values{}, arguments[0], &struct{}{})
	if err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "Session %s runs on this machine now, with what it changed on the worker.\n", session.ID)
	return 0
}

func forwardPort(subcommand string, arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 2 || arguments[0] == "" {
		return usageError(stderr, "worker", subcommand+" takes a session and a port")
	}
	if port, err := strconv.Atoi(arguments[1]); err != nil || port < 1 || port > 65535 {
		return usageError(stderr, "worker", fmt.Sprintf("%q is not a port from 1 to 65535", arguments[1]))
	}
	var forwarded struct{ Port, Local int }
	if _, err := askAboutSession("/workers/"+subcommand, url.Values{"port": {arguments[1]}}, arguments[0], &forwarded); err != nil {
		return failure(stderr, "worker", err)
	}
	if subcommand == "unforward" {
		fmt.Fprintf(stdout, "Port %d of the worker is no longer forwarded.\n", forwarded.Port)
	} else {
		fmt.Fprintf(stdout, "Port %d of the worker is at localhost:%d.\n", forwarded.Port, forwarded.Local)
	}
	return 0
}

// askAboutSession asks the gateway to act on where a session runs, reads its answer into result, and returns the
// session. It waits as long as the gateway takes, such as to copy the session's repository.
func askAboutSession(route string, form url.Values, reference string, result any) (gateway.LocalSession, error) {
	client, err := newGatewayClient()
	if err != nil {
		return gateway.LocalSession{}, err
	}
	session, err := client.session(reference)
	if err != nil {
		return gateway.LocalSession{}, err
	}
	form.Set("session", session.Path)
	request, err := http.NewRequest(http.MethodPost, "http://gripi"+route, strings.NewReader(form.Encode()))
	if err != nil {
		return gateway.LocalSession{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return session, client.do(request, 0, result)
}

func loadWorkers() (config.Config, []worker, error) {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		return cfg, nil, err
	}
	workers, err := readWorkers(cfg.WorkersPath)
	return cfg, workers, err
}

func readWorkers(path string) ([]worker, error) {
	workers := []worker{}
	contents, found, err := state.NewFile(path).Read()
	if err == nil && found {
		if err = json.Unmarshal(contents, &workers); err != nil {
			err = fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return workers, err
}

func saveWorkers(path string, workers []worker) error {
	contents, err := json.MarshalIndent(workers, "", "  ")
	if err != nil {
		return err
	}
	return state.NewFile(path).Write(append(contents, '\n'))
}
