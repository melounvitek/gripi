package helper_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// fakeSudo stands in for sudo, which needs a password and root. Named sudo, it is a parent that gripi's askpass
// program accepts. It asks askpass for the password twice, as sudo does after a wrong one, and records a hash of each
// answer in askpass next to itself. Then it runs the command as this user, who can enter the namespaces of its own
// views only through their user namespace, which root doesn't need.
const fakeSudo = `#!/bin/sh
[ "$1 $2 $3 $4 $5" = "-A -k -p gripi --" ] || { echo "fake sudo: unexpected arguments $*" >&2; exit 99; }
shift 5
for call in 1 2; do
	"$SUDO_ASKPASS" "[sudo: gripi] Password: " | sha256sum | cut -d ' ' -f 1 >> "${0%/*}/askpass"
done
[ "$1" = nsenter ] && shift && set -- nsenter --user --preserve-credentials "$@"
exec "$@"
`

// sudoRun is `gripi worker sudo` as main runs it, under the user's systemd, for a command that the helper prepared.
func (rig *moveHelper) sudoRun(sudo string, offer helper.Message, token string) (command *exec.Cmd, input *os.File, stdout *bufio.Reader, stderr *bytes.Buffer) {
	rig.t.Helper()
	command = exec.Command("systemd-run", "--user", "--pipe", "--quiet", "--collect", buildGripi(rig.t), "worker", "sudo", "--runtime-dir", rig.runtimeDir)
	reader, input, err := os.Pipe()
	if err != nil {
		rig.t.Fatal(err)
	}
	defer reader.Close()
	command.Stdin, stderr = reader, &bytes.Buffer{}
	command.Stderr = stderr
	output, err := command.StdoutPipe()
	if err != nil {
		rig.t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		rig.t.Fatal(err)
	}
	rig.t.Cleanup(func() { input.Close(); command.Wait() })
	if err := json.NewEncoder(input).Encode(helper.SudoRun{Sudo: sudo, Token: token, Cgroup: offer.Path, Args: offer.Args}); err != nil {
		rig.t.Fatal(err)
	}
	return command, input, bufio.NewReader(output), stderr
}

// offerSudo has the helper prepare a command of a session to run with sudo, whose askpass gets password.
func (rig *moveHelper) offerSudo(session, token, password, cwd string, argv ...string) helper.Message {
	rig.t.Helper()
	return rig.request(map[string]any{"type": "sudo.offer", "session": session, "token": token, "password": password, "cwd": cwd, "args": argv})
}

func TestSudoRunsTheCommandInTheSessionsViewAndCgroupWithTheOfferedPassword(t *testing.T) {
	rig := startMoveHelper(t, helper.DefaultLease)
	private := rig.privateRoot()
	shadow, repository := rig.shadow(private, "main's\n")
	rig.move("m1", private, shadow, repository)
	pi := rig.session("s1", "m1", repository)
	pi("mkdir sub")
	sudo := filepath.Join(t.TempDir(), "sudo")
	if err := os.WriteFile(sudo, []byte(fakeSudo), 0700); err != nil {
		t.Fatal(err)
	}

	if refused := rig.offerSudo("s9", "t0", "secret", repository, "true"); refused.OK || !strings.Contains(refused.Error, "Pi") {
		t.Fatalf("sudo.offer for a session that doesn't run = %+v", refused)
	}
	offer := rig.offerSudo("s1", "t1", "secret", filepath.Join(repository, "sub"), "sh", "-c", "pwd; cat ../README; cat /proc/self/cgroup; echo err >&2; exit 3")
	if !offer.OK {
		t.Fatalf("sudo.offer = %+v", offer)
	}
	command, _, output, stderr := rig.sudoRun(sudo, offer, "t1")
	var stdout strings.Builder
	output.WriteTo(&stdout)
	command.Wait()
	lines := strings.Split(stdout.String(), "\n")
	if len(lines) != 4 || lines[0] != filepath.Join(repository, "sub") || lines[1] != "main's" || !strings.HasSuffix(lines[2], ".service/session-s1.m1") || !strings.HasSuffix(stderr.String(), "err\n") || command.ProcessState.ExitCode() != 3 {
		t.Fatalf("the command printed %q and %q, and exited with %d", stdout.String(), stderr, command.ProcessState.ExitCode())
	}
	// A second prompt, as after a wrong password, gets nothing.
	hash := func(text string) string {
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:])
	}
	if answers, _ := os.ReadFile(filepath.Join(filepath.Dir(sudo), "askpass")); !slices.Equal(strings.Fields(string(answers)), []string{hash("secret\n"), hash("")}) {
		t.Fatalf("askpass answered %q", answers)
	}

	// The command stops when main stops waiting for it, and with the session.
	stopped := func(token string, stop func(input *os.File)) {
		t.Helper()
		_, input, output, _ := rig.sudoRun(sudo, rig.offerSudo("s1", token, "secret", repository, "sh", "-c", "echo $$; exec sleep 1000"), token)
		line, _ := output.ReadString('\n')
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			t.Fatalf("the command printed %q", line)
		}
		stop(input)
		assertGone(t, 15*time.Second, pid)
	}
	stopped("t2", func(input *os.File) { input.Close() })
	stopped("t3", func(*os.File) {
		if response := rig.request(map[string]any{"type": "session.release", "session": "s1"}); !response.OK {
			t.Fatalf("session.release = %+v", response)
		}
	})
}
