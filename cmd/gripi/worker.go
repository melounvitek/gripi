package main

import (
	"cmp"
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
	"github.com/melounvitek/gripi/internal/state"
	remote "github.com/melounvitek/gripi/internal/worker"
)

const workerHelp = `Usage:
  gripi worker add <user@host> [--name <name>]
  gripi worker list [--json]
  gripi worker remove <name>
  gripi worker move <session or folder> <name>
  gripi worker return <session or folder>
  gripi worker forward <session or folder> <port>
  gripi worker unforward <session or folder> <port>

Internal, run by Gripi on a worker over SSH:
  gripi worker serve [--lease <duration>] [--runtime-dir <folder>]
  gripi worker attach [--runtime-dir <folder>] <session> <helper instance>
  gripi worker receive [--aside <folder>] <folder on main> <folder>
  gripi worker mirror [--runtime-dir <folder>] <unit> <helper instance>
  gripi worker enter [--home <folder>] -- <command>

A worker is another computer that runs sessions for this gateway. Gripi
reaches it with this machine's ssh, as the gateway's user and with that
user's SSH configuration. Workers belong to the whole gateway. add, list and
remove need no running gateway; move, return, forward and unforward do.

add connects to the worker without asking for anything, so ssh must already
trust the worker's host key and log in with a key. It checks that the worker
has Git, the same operating system and architecture as this machine, and a
systemd user manager with cgroup v2, which lets Gripi stop everything a
session started.
Sessions use the same paths on the worker as here, so add fails when the
worker's user cannot create Pi's folder (~/.pi/agent) at its path here, and
warns when the worker's home folder has another path. It also fails when the
worker shares this machine's files, as this machine itself or a home folder
mounted on both would. The worker will hold a full copy of Pi's
configuration, including logins; keeping it secure is up to you.

list prints each worker's name and SSH destination.

remove forgets a worker, unless repositories are on it. Files on the worker
stay there.

move runs a repository's sessions on a worker from now on: the repository of
the folder, or of the session's folder, with all its worktrees, or the folder
alone outside Git. It needs a running gateway, which stops the Pi of each
session there, copies the repository and the sessions to the worker, and
keeps copies of both here up to date. The copy of the repository here is
read-only: what changes in it anyway is saved aside before the worker's
changes replace it. move fails while one of the sessions is working. return
brings the repository and its sessions back to this machine. When this
machine has lost the worker, return waits until the worker must have stopped
the sessions there, and says when that is; then the repository continues
here as this machine last got it, and the next move to the worker keeps
what the worker had beyond that aside there.

forward makes a port on the worker that runs the repository's sessions
available on this machine, such as a dev server that a session started
there: at the same port on localhost, or at the next free one, which it
prints. Both ends listen only on localhost. The forward lasts until
unforward, or until this machine's connection to the worker ends.

serve is the helper that runs the worker's sessions, and stops all of them
when this machine stops answering. attach connects to the Pi of a session.
receive makes a folder a copy of one that this machine sends, or, given the
same folder twice, a copy of its repository at the same paths, and keeps
what it replaces or deletes in the folder of --aside first. mirror
connects this machine's copy of a repository, or of its sessions, to the
helper, which sends their changes. enter runs a command in a private view in
which the folder of --home, this machine's home folder, shows the worker
user's own home folder, unless that is the same folder.

Gripi keeps the workers in ~/.pi/gripi/workers.json, or in the file named by
GRIPI_WORKERS_PATH, and which repositories are on which worker in
~/.pi/gripi/worker-placements.json, or in GRIPI_WORKER_PLACEMENTS_PATH.

Arguments:
  user@host  Where ssh connects, as you would pass it to ssh; a Host from
             your SSH configuration works too
  name       Name of a worker
  session    Session ID, a unique prefix of it, or the session file path
  folder     A folder in the repository
  port       A port on the worker's localhost

Flags:
  --name  Name of the new worker; defaults to the first part of the host name,
          such as beelink for gripi@beelink.example.ts.net
  --json  Print a JSON array of {"name", "destination"} objects instead of a
          table

Examples:
  gripi worker add gripi@beelink.example.ts.net
  gripi worker move ~/Work/project beelink
  gripi worker return 01a107aa
  gripi worker forward 01a107aa 5173
  gripi worker remove beelink

Exit codes:
  0  success
  1  the worker cannot be reached or cannot run sessions, the name is taken,
     no worker has the name, repositories are on the worker that remove
     names, or the gateway refused or failed to move the repository or to
     forward the port
  2  usage error
`

type worker struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

// workerProbe prints the worker's system, architecture, user and home, then "git" if it has Git, "systemd" if
// systemd-run can start a command under the user's systemd with cgroup v2, and "agent" if the user can create and
// write to Pi's agent folder at its path on main.
// ssh runs it in the worker user's shell, so it uses only what both sh and fish accept.
func workerProbe(agentDir string) string {
	return `uname -s; uname -m; id -un; echo "$HOME"; command -v git >/dev/null && echo git; ` +
		`systemd-run --user --pipe --quiet test -f /sys/fs/cgroup/cgroup.controllers && echo systemd; ` +
		"mkdir -p " + remote.ShellQuote(agentDir) + " && test -w " + remote.ShellQuote(agentDir) + " && echo agent"
}

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
		return moveRepository(arguments[1:], stdout, stderr)
	case "return":
		return returnRepository(arguments[1:], stdout, stderr)
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
	case "enter":
		return enterWorker(arguments[1:], stdin, stdout, stderr)
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

	lines, err := probeWorker(destination, workerProbe(cfg.PiAgentDir))
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
	if !slices.Contains(lines[4:], "agent") {
		err := fmt.Errorf("%s cannot create Pi's folder %s on %s, or cannot write to it.\nSessions use the same paths on the worker as here.", user, cfg.PiAgentDir, destination)
		if home != cfg.Home {
			err = fmt.Errorf(`%w
The home folder of %s there is %s, but here it is %s. Move it by running
this on the worker as an administrator, while %s is logged out:
  sudo usermod -m -d %s %s`, err, user, home, cfg.Home, user, cfg.Home, user)
		}
		return failure(stderr, "worker", err)
	}
	if err := remote.CheckSeparate(destination, func(script string, arguments ...string) (string, error) {
		remoteCommand := remote.ShellQuote(append([]string{"sh", "-c", script, "sh"}, arguments...)...)
		output, err := exec.Command("ssh", "-o", "BatchMode=yes", "--", destination, remoteCommand).Output()
		return string(output), err
	}, cfg.PiAgentDir, filepath.Dir(cfg.SocketPath)); err != nil {
		return failure(stderr, "worker", err)
	}
	if home != cfg.Home {
		fmt.Fprintf(stderr, `Warning: the home folder of %s on the worker is %s, but here it is %s.
Sessions use the same paths on the worker, so move the home folder by running
this on the worker as an administrator, while %s is logged out:
  sudo usermod -m -d %s %s
or create the project folders at the same paths there.
`, user, home, cfg.Home, user, cfg.Home, user)
	}

	if err := saveWorkers(cfg.WorkersPath, append(workers, worker{Name: *name, Destination: destination})); err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, `Added worker %s (%s).
The worker will hold a full copy of Pi's configuration, including logins.
Keeping it secure is up to you.
`, *name, destination)
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
			return failure(stderr, "worker", fmt.Errorf("%s is on %s; move it back with 'gripi worker return %s' first", placement.Unit.Main, name, placement.Unit.Main))
		}
	}
	if err := saveWorkers(cfg.WorkersPath, slices.Delete(workers, index, index+1)); err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "Removed worker %s. Files on the worker stay there.\n", name)
	return 0
}

func moveRepository(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 2 || arguments[0] == "" || arguments[1] == "" {
		return usageError(stderr, "worker", "move takes a session or folder and a worker name")
	}
	folder, err := askAboutRepository("/workers/move", url.Values{"worker": {arguments[1]}}, arguments[0], &struct{}{})
	if err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "The sessions of the repository of %s run on %s now.\n", folder, arguments[1])
	return 0
}

func returnRepository(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 1 || arguments[0] == "" {
		return usageError(stderr, "worker", "return takes a session or folder")
	}
	folder, err := askAboutRepository("/workers/return", url.Values{}, arguments[0], &struct{}{})
	if err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "The sessions of the repository of %s run on this machine now.\n", folder)
	return 0
}

func forwardPort(subcommand string, arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 2 || arguments[0] == "" {
		return usageError(stderr, "worker", subcommand+" takes a session or folder and a port")
	}
	if port, err := strconv.Atoi(arguments[1]); err != nil || port < 1 || port > 65535 {
		return usageError(stderr, "worker", fmt.Sprintf("%q is not a port from 1 to 65535", arguments[1]))
	}
	var forwarded struct{ Port, Local int }
	if _, err := askAboutRepository("/workers/"+subcommand, url.Values{"port": {arguments[1]}}, arguments[0], &forwarded); err != nil {
		return failure(stderr, "worker", err)
	}
	if subcommand == "unforward" {
		fmt.Fprintf(stdout, "Port %d of the worker is no longer forwarded.\n", forwarded.Port)
	} else {
		fmt.Fprintf(stdout, "Port %d of the worker is at localhost:%d.\n", forwarded.Port, forwarded.Local)
	}
	return 0
}

// askAboutRepository asks the gateway to act on the repository of a folder, or of a session's folder, reads its
// answer into result, and returns the folder. It waits as long as the gateway takes, such as to copy the repository.
func askAboutRepository(route string, form url.Values, reference string, result any) (string, error) {
	client, err := newGatewayClient()
	if err != nil {
		return "", err
	}
	folder, _ := filepath.Abs(reference)
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		session, err := client.session(reference)
		if err != nil {
			return "", err
		}
		folder = session.CWD
	}
	form.Set("folder", folder)
	request, err := http.NewRequest(http.MethodPost, "http://gripi"+route, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return folder, client.do(request, 0, result)
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
