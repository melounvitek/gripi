// Package view lets processes on a worker see main's home folder as their user's own. When the worker user's home
// folder has another path than main's, what Gripi runs there with main's paths, such as the helper with Pi and the
// copies of main's folders, runs in a private view of the file system: main's home folder shows the worker user's
// home folder, and everything else stays as it is.
package view

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Needed reports whether this process needs the view to see home, main's home folder, as its own: whether home is set
// and differs from $HOME. Inside the view, $HOME is main's home folder, so a process there needs no other view.
func Needed(home string) bool {
	if home == "" {
		return false
	}
	own, err := os.UserHomeDir()
	return err != nil || filepath.Clean(own) != filepath.Clean(home)
}

// Run runs a command in the view of home, main's home folder, with the given input and outputs, and returns its exit
// status. The command runs as this user, with HOME set to home, in the current folder if the view has it and in
// home otherwise. When the view is not needed, the command runs as it is.
func Run(home string, command []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	if len(command) == 0 {
		return 0, errors.New("no command to run")
	}
	process := exec.Command(command[0], command[1:]...)
	needed := Needed(home)
	if needed {
		var err error
		if process, err = viewCommand(home, command); err != nil {
			return 0, err
		}
	}
	process.Stdin, process.Stdout, process.Stderr = stdin, stdout, stderr
	code, err := run(process)
	if err != nil && needed && process.Process == nil {
		return 0, fmt.Errorf("cannot enter a private view that shows this user's home folder at %s (%w). It needs unprivileged user namespaces, which this system blocks; on Ubuntu, allow them with 'sudo sysctl kernel.apparmor_restrict_unprivileged_userns=0', and in a file in /etc/sysctl.d to keep that after a restart", home, err)
	}
	return code, err
}

// Move is the view of a moved session's private copy of its repository: each of its private roots, folders directly
// in main's home folder, shows the move's copy of it, and everything else stays as the process that starts the view
// sees it, such as Pi's agent folder in the view of main's home folder.
type Move struct {
	Home    string   // main's home folder
	Copy    string   // the move's copy, which holds the private roots under their names
	Private []string // the private roots
}

// Command returns a command that runs command in the move's view, as this user with HOME set to main's home folder,
// in the folder dir, which must exist there, and with env, or this process's environment when env is nil. It always
// starts this executable again, which makes the view, also where this process sees main's home folder as its own.
func (move Move) Command(dir string, command, env []string) (*exec.Cmd, error) {
	if len(command) == 0 {
		return nil, errors.New("no command to run")
	}
	if len(move.Private) == 0 {
		return nil, errors.New("a move's view needs private roots")
	}
	for _, root := range move.Private {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root || filepath.Dir(root) != filepath.Clean(move.Home) {
			return nil, fmt.Errorf("the private root %q is not a folder directly in main's home folder %q", root, move.Home)
		}
	}
	return moveCommand(move, dir, command, env)
}

// run starts process, passes on the signals that would stop this process, and returns the process's exit status, or
// 128 plus the signal that killed it.
func run(process *exec.Cmd) (int, error) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer func() {
		signal.Stop(signals)
		close(signals)
	}()
	if err := process.Start(); err != nil {
		return 0, err
	}
	go func() {
		for received := range signals {
			process.Process.Signal(received)
		}
	}()
	err := process.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 0, err
}
