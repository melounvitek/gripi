package main

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/state"
)

const workerHelp = `Usage:
  gripi worker add <user@host> [--name <name>]
  gripi worker list [--json]
  gripi worker remove <name>

A worker is another computer that runs sessions for this gateway. Gripi
reaches it with this machine's ssh, as the gateway's user and with that
user's SSH configuration. Workers belong to the whole gateway, and these
commands need no running gateway.

add connects to the worker without asking for anything, so ssh must already
trust the worker's host key and log in with a key. It checks that the worker
has Git, the same operating system and architecture as this machine, and a
systemd user manager with cgroup v2, which lets Gripi stop everything a
session started.
Sessions use the same paths on the worker as here, so add warns when the
worker's home folder has another path. The worker will hold a full copy of
Pi's configuration, including logins; keeping it secure is up to you.

list prints each worker's name and SSH destination.

remove forgets a worker. Files on the worker stay there.

Gripi keeps the workers in ~/.pi/gripi/workers.json, or in the file named by
GRIPI_WORKERS_PATH.

Arguments:
  user@host  Where ssh connects, as you would pass it to ssh; a Host from
             your SSH configuration works too
  name       Name of a worker

Flags:
  --name  Name of the new worker; defaults to the first part of the host name,
          such as beelink for gripi@beelink.example.ts.net
  --json  Print a JSON array of {"name", "destination"} objects instead of a
          table

Examples:
  gripi worker add gripi@beelink.example.ts.net
  gripi worker remove beelink

Exit codes:
  0  success
  1  the worker cannot be reached or cannot run sessions, the name is taken,
     or no worker has the name
  2  usage error
`

type worker struct {
	Name        string `json:"name"`
	Destination string `json:"destination"`
}

// workerProbe prints the worker's system, architecture, user and home, then "git" if it has Git and "systemd" if
// systemd-run can start a command under the user's systemd with cgroup v2.
// ssh runs it in the worker user's shell, so it uses only what both sh and fish accept.
const workerProbe = `uname -s; uname -m; id -un; echo "$HOME"; command -v git >/dev/null && echo git; ` +
	`systemd-run --user --pipe --quiet test -f /sys/fs/cgroup/cgroup.controllers && echo systemd`

func manageWorkers(arguments []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) == 0 {
		return usageError(stderr, "worker", "needs add, list or remove")
	}
	switch arguments[0] {
	case "add":
		return addWorker(arguments[1:], stdout, stderr)
	case "list":
		return listWorkers(arguments[1:], stdout, stderr)
	case "remove":
		return removeWorker(arguments[1:], stdout, stderr)
	}
	return usageError(stderr, "worker", fmt.Sprintf("unknown subcommand %q; use add, list or remove", arguments[0]))
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

	lines, err := probeWorker(destination)
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
	if user, home := lines[2], lines[3]; home != cfg.Home {
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

// probeWorker runs workerProbe on the worker and returns the lines it printed.
func probeWorker(destination string) ([]string, error) {
	command := exec.Command("ssh", "-o", "BatchMode=yes", "--", destination, workerProbe)
	var errorOutput strings.Builder
	command.Stderr = &errorOutput
	output, err := command.Output()
	var exit *exec.ExitError
	// ssh exits with 255 when it fails, and otherwise with the probe's status, which is 1 without systemd.
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
	if err := saveWorkers(cfg.WorkersPath, slices.Delete(workers, index, index+1)); err != nil {
		return failure(stderr, "worker", err)
	}
	fmt.Fprintf(stdout, "Removed worker %s. Files on the worker stay there.\n", name)
	return 0
}

func loadWorkers() (config.Config, []worker, error) {
	cfg, err := config.Load(os.Environ())
	if err != nil {
		return cfg, nil, err
	}
	workers := []worker{}
	contents, found, err := state.NewFile(cfg.WorkersPath).Read()
	if err == nil && found {
		if err = json.Unmarshal(contents, &workers); err != nil {
			err = fmt.Errorf("parse %s: %w", cfg.WorkersPath, err)
		}
	}
	return cfg, workers, err
}

func saveWorkers(path string, workers []worker) error {
	contents, err := json.MarshalIndent(workers, "", "  ")
	if err != nil {
		return err
	}
	return state.NewFile(path).Write(append(contents, '\n'))
}
