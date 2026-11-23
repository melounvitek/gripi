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
// prepares it (sudo.offer), and `gripi worker sudo`, which main runs under the user's systemd, outside every view,
// where setuid works, starts sudo in the session's cgroup. sudo runs nsenter, which enters the mount namespace of the
// session's Pi, its view, and runs the command there. Each time that sudo asks for the password, its askpass program,
// gripi, asks the helper on askpassSocket, which asks main (sudo.askpass). The first time, main gives the password
// that the user entered, and after sudo rejected it, the one that the user enters again (sudo.password).

// askpassSocket is where sudo's askpass program, gripi, takes the password, in the runtime folder. It answers as the
// gateway's socket does, so gripi needs nothing else there.
const askpassSocket = "askpass.sock"

// sudoPrompt is the prompt that gripi sudo gives sudo, which gripi, as askpass, answers (server.SudoAskpassPrompts).
const sudoPrompt = "gripi"

// askpassTimeout bounds how long sudo's askpass program waits for main, which waits at most 2 minutes for the user.
const askpassTimeout = 5 * time.Minute

// SudoRun is what `gripi worker sudo` gets from main on the first line of its input.
type SudoRun struct {
	Sudo   string   `json:"sudo,omitempty"` // /usr/bin/sudo when empty; tests use a fake one
	Token  string   `json:"token"`          // with which sudo's askpass program asks main for the password
	Cgroup string   `json:"cgroup"`         // the session's
	Args   []string `json:"args"`           // what sudo runs as root
}

// offerSudo returns the cgroup of a session for which main runs a command as root, and what sudo runs: nsenter, which
// runs the command at cwd in the mount namespace of the session's Pi.
func (s *server) offerSudo(request Message) (cgroup string, args []string, err error) {
	if len(request.Args) == 0 || !filepath.IsAbs(request.Cwd) {
		return "", nil, errors.New("sudo.offer needs a command and an absolute folder")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.sessions[request.Session]
	if current == nil || current.pi == nil {
		return "", nil, errors.New("the session's Pi doesn't run on the worker")
	}
	nsenter := []string{"nsenter", "--target", strconv.Itoa(current.pi.Process.Pid), "--mount", "--wdns=" + request.Cwd, "--"}
	return current.cgroup, append(nsenter, request.Args...), nil
}

// answerAskpass asks main for the password of sudo's askpass program, gripi, by its token, and gives it what main
// answers, or nothing when main refuses.
func (s *server) answerAskpass(response http.ResponseWriter, request *http.Request) {
	token := request.PostFormValue("token")
	answer := make(chan []byte, 1)
	s.mu.Lock()
	_, asking := s.askpasses[token]
	if !asking {
		s.askpasses[token] = answer
	}
	s.mu.Unlock()
	if asking {
		http.NotFound(response, request)
		return
	}
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.askpasses[token] == answer {
			delete(s.askpasses, token)
		}
		// An answer that came as the wait ended.
		select {
		case password := <-answer:
			clear(password)
		default:
		}
	}()
	s.send(Message{Type: "sudo.askpass", Token: token})
	timeout := time.NewTimer(askpassTimeout)
	defer timeout.Stop()
	var password []byte
	select {
	case password = <-answer:
	case <-timeout.C:
	case <-request.Context().Done():
	case <-s.done:
	}
	if len(password) == 0 {
		http.NotFound(response, request)
		return
	}
	defer clear(password)
	response.Write(password)
}

// givePassword passes main's answer on to the askpass program that asked for it.
func (s *server) givePassword(request Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	answer, asking := s.askpasses[request.Token]
	if !asking {
		return errors.New("no askpass program waits for this token")
	}
	delete(s.askpasses, request.Token)
	answer <- []byte(request.Password)
	return nil
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
	// Classic sudo 1.9.5p2 to 1.9.12 drops a SIGTERM from its own process group, so sudo gets a group of its own.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
