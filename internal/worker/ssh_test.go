package worker

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeSSHScript stands in for ssh. It logs its arguments, keeps a master running for -M, answers -O check, accepts
// other -O operations, and runs remote commands on this machine, in FAKE_SSH_HOME.
const fakeSSHScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SSH_LOG"
socket= operation= master=
while [ "$1" != -- ]; do
	case $1 in
	-S) socket=$2; shift ;;
	-O) operation=$2; shift ;;
	-o | -R | -L) shift ;;
	-M*) master=yes ;;
	esac
	shift
done
shift 2
if [ -n "$master" ]; then
	echo $$ > "$socket"
	exec sleep 1000
fi
case $operation in
check) exec kill -0 "$(cat "$socket")" 2>/dev/null ;;
'') cd "$FAKE_SSH_HOME" && HOME=$FAKE_SSH_HOME exec sh -c "$*" ;;
esac
`

// fakeSSH puts fakeSSHScript on PATH as ssh and returns the worker's home.
func fakeSSH(t *testing.T) (home string) {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(fakeSSHScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSH_LOG", filepath.Join(directory, "log"))
	home = t.TempDir()
	t.Setenv("FAKE_SSH_HOME", home)
	return home
}

// sshCalls returns the arguments of every ssh run so far that contain all of words.
func sshCalls(t *testing.T, words ...string) []string {
	t.Helper()
	contents, _ := os.ReadFile(os.Getenv("FAKE_SSH_LOG"))
	var calls []string
	for _, call := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if !slices.ContainsFunc(words, func(word string) bool { return !strings.Contains(call, word) }) {
			calls = append(calls, call)
		}
	}
	return calls
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	pool := NewPool("gripi@worker", t.TempDir())
	t.Cleanup(pool.Close)
	return pool
}

func socketOf(arguments []string) string {
	return arguments[slices.Index(arguments, "-S")+1]
}

func TestPoolOpensUpToEightChannelsPerMaster(t *testing.T) {
	fakeSSH(t)
	pool := newTestPool(t)
	ctx := context.Background()
	perSocket := map[string]int{}
	var first string
	var releases []func()
	for range MaxChannels + 1 {
		command, release, err := pool.Command(ctx, "echo hello")
		if err != nil {
			t.Fatal(err)
		}
		first = cmp.Or(first, socketOf(command.Args))
		perSocket[socketOf(command.Args)]++
		releases = append(releases, release)
	}
	if len(perSocket) != 2 || perSocket[first] != MaxChannels || len(sshCalls(t, "-MNnT")) != 2 {
		t.Fatalf("channels per master socket = %v, masters started %q", perSocket, sshCalls(t, "-MNnT"))
	}

	releases[0]()
	releases[0]()
	command, _, err := pool.Command(ctx, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if expected := []string{"ssh", "-S", first, "-o", "ControlMaster=no", "-o", "BatchMode=yes", "-T", "--", "gripi@worker", "echo hello"}; !reflect.DeepEqual(command.Args, expected) {
		t.Fatalf("channel = %q, want %q", command.Args, expected)
	}
	if output, err := command.Output(); err != nil || string(output) != "hello\n" {
		t.Fatalf("channel output = %q, %v", output, err)
	}
	if command, _, _ := pool.Command(ctx, "echo hello"); socketOf(command.Args) == first {
		t.Fatal("a released channel was counted free twice")
	}
	if len(sshCalls(t, "-O check")) < MaxChannels+3 {
		t.Fatalf("channels opened without checking their master: %q", sshCalls(t, "-O check"))
	}
}

func TestPoolReplacesAMasterThatEnded(t *testing.T) {
	fakeSSH(t)
	pool := newTestPool(t)
	command, _, err := pool.Command(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := os.ReadFile(socketOf(command.Args))
	master, _ := strconv.Atoi(strings.TrimSpace(string(pid)))
	syscall.Kill(master, syscall.SIGKILL)
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat("/proc/" + strconv.Itoa(master)); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the master is still running")
		}
	}

	again, _, err := pool.Command(context.Background(), "true")
	if err != nil || socketOf(again.Args) == socketOf(command.Args) {
		t.Fatalf("channel after the master ended = %q, %v", again.Args, err)
	}
}

func TestPoolFailsWhenTheMasterCannotConnect(t *testing.T) {
	fakeSSH(t)
	directory := t.TempDir()
	os.WriteFile(filepath.Join(directory, "ssh"), []byte("#!/bin/sh\necho 'ssh: Could not resolve hostname worker' >&2\nexit 255\n"), 0700)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := newTestPool(t).Command(context.Background(), "true")
	if err == nil || !strings.Contains(err.Error(), "Could not resolve hostname worker") {
		t.Fatalf("Command = %v", err)
	}
}

func TestForwardsAreReferenceCounted(t *testing.T) {
	fakeSSH(t)
	pool := newTestPool(t)
	ctx := context.Background()
	proxy := Forward{Remote: true, Listen: 8317, Connect: 8317}

	cancelFirst, err := pool.Forward(ctx, proxy)
	if err != nil {
		t.Fatal(err)
	}
	cancelSecond, _ := pool.Forward(ctx, proxy)
	devServer, _ := pool.Forward(ctx, Forward{Listen: 3001, Connect: 3000})
	if forwards := sshCalls(t, "-O forward"); len(forwards) != 2 ||
		!strings.Contains(forwards[0], "-R 127.0.0.1:8317:127.0.0.1:8317 -- gripi@worker") ||
		!strings.Contains(forwards[1], "-L 127.0.0.1:3001:127.0.0.1:3000 -- gripi@worker") {
		t.Fatalf("forwards = %q", forwards)
	}
	cancelFirst()
	cancelFirst()
	if cancels := sshCalls(t, "-O cancel"); len(cancels) != 0 {
		t.Fatalf("cancelled a forward still in use: %q", cancels)
	}
	cancelSecond()
	devServer()
	if cancels := sshCalls(t, "-O cancel"); len(cancels) != 2 || !strings.Contains(cancels[0], "-R 127.0.0.1:8317:127.0.0.1:8317") {
		t.Fatalf("cancels = %q", cancels)
	}
	if again, _ := pool.Forward(ctx, proxy); len(sshCalls(t, "-O forward")) != 3 {
		t.Fatalf("forward after cancel = %q", sshCalls(t, "-O forward"))
	} else {
		again()
	}
}
