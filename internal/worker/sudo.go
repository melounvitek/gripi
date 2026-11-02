package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// Sudo runs a command as root on the worker that runs a session, in the session's view at cwd, with the worker's
// sudo, which gets password, the worker user's. It passes the command's outputs to stdout and stderr, stops sudo when
// ctx ends, and returns sudo's exit status.
func (manager *Manager) Sudo(ctx context.Context, key, cwd string, argv []string, password []byte, stdout, stderr io.Writer) (int, error) {
	placement, placed := manager.options.Placements.Find(key)
	if !placed {
		return 0, refuse("the session runs on main now; run the command again")
	}
	if placement.Stage != "" {
		return 0, refuse("the session is coming back from worker %s; run the command again once it runs on main", placement.Worker)
	}
	manager.mu.Lock()
	var conn link
	if worker := manager.workers[placement.Worker]; worker != nil {
		conn = worker.conn
	}
	manager.mu.Unlock()
	if conn == nil {
		return 0, fmt.Errorf("worker %s disconnected; run the command again once Gripi reaches it", placement.Worker)
	}
	return conn.Sudo(ctx, sessionID(key), cwd, argv, password, stdout, stderr)
}

// Sudo runs a command as root in the view of a session on the worker, at cwd: the helper keeps password for the
// askpass program of the worker's sudo, which `gripi worker sudo` runs outside the views, in the session's cgroup. It
// passes the command's outputs to stdout and stderr, stops sudo when ctx ends, and returns sudo's exit status.
func (conn *Conn) Sudo(ctx context.Context, session, cwd string, argv []string, password []byte, stdout, stderr io.Writer) (int, error) {
	random := make([]byte, 16)
	rand.Read(random)
	token := hex.EncodeToString(random)
	offer, err := conn.request(ctx, helper.Message{Type: "sudo.offer", Session: session, Token: token, Password: string(password), Cwd: cwd, Args: argv})
	if err != nil {
		return 0, err
	}
	run, err := json.Marshal(helper.SudoRun{Sudo: conn.options.Sudo, Token: token, Cgroup: offer.Path, Args: offer.Args})
	if err != nil {
		return 0, err
	}
	// Under the user's systemd, it may start sudo in the session's cgroup, unlike in ssh's session.
	words := []string{"systemd-run", "--user", "--pipe", "--quiet", "--collect", conn.installDir + "/bin/gripi", "worker", "sudo"}
	if conn.options.RuntimeDir != "" {
		words = append(words, "--runtime-dir", conn.options.RuntimeDir)
	}
	command, release, err := conn.Command(ctx, ShellQuote(words...))
	if err != nil {
		return 0, err
	}
	defer release()
	input, err := command.StdinPipe()
	if err != nil {
		return 0, err
	}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return 0, err
	}
	input.Write(append(run, '\n'))
	// gripi worker sudo stops sudo once its input ends.
	stop := context.AfterFunc(ctx, func() { input.Close() })
	defer stop()
	if err := command.Wait(); command.ProcessState == nil {
		return 0, err
	}
	return command.ProcessState.ExitCode(), nil
}
