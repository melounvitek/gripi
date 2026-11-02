package helper

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// A session's command that `gripi sudo` asks for runs as root on the worker, and sees the session's paths: the helper
// keeps the password that main offers for it (sudo.offer), and `gripi worker sudo`, which main runs under the
// user's systemd, outside every view, where setuid works, starts sudo in the session's cgroup. sudo runs nsenter,
// which enters the mount namespace of the session's Pi, its view, and runs the command there. sudo's askpass program,
// gripi, gets the password from the helper on askpassSocket.

// askpassSocket is where sudo's askpass program, gripi, takes the password, in the runtime folder. It answers as the
// gateway's socket does, so gripi needs nothing else there.
const askpassSocket = "askpass.sock"

// sudoPrompt is the prompt that gripi sudo gives sudo, which gripi, as askpass, answers (server.SudoAskpassPrompt).
const sudoPrompt = "gripi"

// passwordLifetime bounds how long the helper keeps a password that sudo's askpass program hasn't taken.
const passwordLifetime = time.Minute

// SudoRun is what `gripi worker sudo` gets from main on the first line of its input.
type SudoRun struct {
	Sudo   string   `json:"sudo,omitempty"` // /usr/bin/sudo when empty; tests use a fake one
	Token  string   `json:"token"`          // for the password that the helper keeps
	Cgroup string   `json:"cgroup"`         // the session's
	Args   []string `json:"args"`           // what sudo runs as root
}

// offerSudo keeps the password of a command that main runs as root for a session, until sudo's askpass program takes
// it, and returns the session's cgroup and what sudo runs: nsenter, which runs the command at cwd in the mount
// namespace of the session's Pi.
func (s *server) offerSudo(request Message) (cgroup string, args []string, err error) {
	if request.Token == "" || len(request.Args) == 0 || !filepath.IsAbs(request.Cwd) {
		return "", nil, errors.New("sudo.offer needs a token, a command and an absolute folder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.sessions[request.Session]
	if current == nil || current.pi == nil {
		return "", nil, errors.New("the session's Pi doesn't run on the worker")
	}
	s.passwords[request.Token] = []byte(request.Password)
	time.AfterFunc(passwordLifetime, func() { clear(s.takePassword(request.Token)) })
	nsenter := []string{"nsenter", "--target", strconv.Itoa(current.pi.Process.Pid), "--mount", "--wdns=" + request.Cwd, "--"}
	return current.cgroup, append(nsenter, request.Args...), nil
}

// takePassword hands out the password for a token once, to a caller that then owns it.
func (s *server) takePassword(token string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	password := s.passwords[token]
	delete(s.passwords, token)
	return password
}

// answerAskpass gives sudo's askpass program, gripi, the password for its token.
func (s *server) answerAskpass(response http.ResponseWriter, request *http.Request) {
	password := s.takePassword(request.PostFormValue("token"))
	if password == nil {
		http.NotFound(response, request)
		return
	}
	defer clear(password)
	response.Write(password)
}

// Sudo runs sudo for the helper whose runtime folder is runtimeDir as the first line of input says, as
// `gripi worker sudo` does for main, and passes its outputs to stdout and stderr. Once input ends, it stops sudo, which
// passes SIGTERM on to the command. It returns sudo's exit status.
func Sudo(runtimeDir string, input io.Reader, stdout, stderr io.Writer) (int, error) {
	lines := bufio.NewReader(input)
	line, err := lines.ReadBytes('\n')
	var run SudoRun
	if err == nil {
		err = json.Unmarshal(line, &run)
	}
	if err != nil {
		return 0, fmt.Errorf("read the command: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		io.Copy(io.Discard, lines)
		cancel()
	}()
	command := exec.CommandContext(ctx, cmp.Or(run.Sudo, "/usr/bin/sudo"), append([]string{"-A", "-k", "-p", sudoPrompt, "--"}, run.Args...)...)
	// Stdin stays nil, which reads /dev/null.
	command.Env = append(os.Environ(), "SUDO_ASKPASS="+executable, "GRIPI_SUDO_TOKEN="+run.Token, "GRIPI_SOCKET_PATH="+filepath.Join(runtimeDir, askpassSocket))
	// Through pipes, which WaitDelay stops reading, so that what the command leaves running can't keep main waiting.
	command.Stdout, command.Stderr = struct{ io.Writer }{stdout}, struct{ io.Writer }{stderr}
	command.Cancel = func() error { return command.Process.Signal(syscall.SIGTERM) }
	command.WaitDelay = 10 * time.Second
	if err := startInCgroup(command, run.Cgroup); err != nil {
		return 0, fmt.Errorf("start sudo: %w", err)
	}
	if err := command.Wait(); command.ProcessState == nil {
		return 0, err
	}
	status := command.ProcessState.Sys().(syscall.WaitStatus)
	if status.Signaled() {
		return 128 + int(status.Signal()), nil
	}
	return status.ExitStatus(), nil
}
