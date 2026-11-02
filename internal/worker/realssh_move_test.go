package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// evalPi stands in for Pi in a move: it runs each line of its input as a command, in the move's view, and ends what
// the command printed with a line "--".
const evalPi = "#!/bin/sh\nwhile read -r line; do eval \"$line\"; echo --; done\n"

// gitIn runs Git in dir, with no configuration of the user's.
func gitIn(t *testing.T, dir string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

// serveMirror connects destination to one of a move's mirrors, which it starts first, and returns what Serve returns.
func serveMirror(t *testing.T, conn *Conn, move, file string, destination *mirror.Destination) <-chan error {
	t.Helper()
	ctx := context.Background()
	if err := conn.StartMoveMirror(ctx, move, file); err != nil {
		t.Fatal(err)
	}
	command, release, err := conn.AttachMoveMirror(ctx, move, file)
	if err != nil {
		t.Fatal(err)
	}
	input, _ := command.StdinPipe()
	output, _ := command.StdoutPipe()
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		release()
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		err := destination.Serve(ctx, struct {
			io.Reader
			io.WriteCloser
		}{output, input}, t.TempDir())
		served <- errors.Join(err, command.Wait())
		release()
	}()
	return served
}

// A session moves to a worker whose user's home folder differs from main's, on a private copy of its repository: its
// Pi sees the copy at main's paths, also a worktree that it makes there, main gets what the session changed until
// its processes stopped, and the folders that Git ignores stay on the worker for the next copy.
func TestRealSSHMovesASessionOnACopyOfItsRepository(t *testing.T) {
	worker := connectRealWorker(t, 0)
	ctx := context.Background()
	conn, home := worker.conn, worker.options.Home
	name := make([]byte, 4)
	rand.Read(name)
	private := filepath.Join(home, "gripi-move-test-"+hex.EncodeToString(name))
	sessions := t.TempDir()
	// The worker made the folders above the session's too.
	t.Cleanup(func() {
		worker.direct("rm -rf " + ShellQuote(conn.workerPath(private)) + " " + ShellQuote(filepath.Dir(sessions)))
	})

	// Main's shadow of the repository, as it moves with a session that started in a linked worktree: that worktree
	// and the common .git, whose links name main's paths.
	shadow := t.TempDir()
	repository, feature := filepath.Join(private, "repo"), filepath.Join(private, "feature")
	if err := os.MkdirAll(shadow+repository, 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, shadow+repository, "init", "-q", "-b", "main")
	os.WriteFile(shadow+filepath.Join(repository, "README"), []byte("main's\n"), 0600)
	os.WriteFile(shadow+filepath.Join(repository, ".gitignore"), []byte("node_modules/\n"), 0600)
	gitIn(t, shadow+repository, "add", "-A")
	gitIn(t, shadow+repository, "commit", "-qm", "first")
	gitIn(t, shadow+repository, "worktree", "add", "-q", "-b", "feature", shadow+feature)
	for path, contents := range map[string]string{
		shadow + filepath.Join(feature, ".git"):                                      "gitdir: " + filepath.Join(repository, ".git", "worktrees", "feature") + "\n",
		shadow + filepath.Join(repository, ".git", "worktrees", "feature", "gitdir"): filepath.Join(feature, ".git") + "\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, unselected := range []string{"README", ".gitignore"} {
		os.Remove(shadow + filepath.Join(repository, unselected))
	}
	file := filepath.Join(sessions, "session.jsonl")
	if err := os.WriteFile(file, []byte("{\"type\":\"session\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}

	move := Move{ID: "m1", Repository: filepath.Join(repository, ".git"), Roots: []string{feature}, Private: []string{private}}
	copy, err := conn.PrepareMove(ctx, move)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.UploadMove(ctx, "m1", shadow+home); err != nil {
		t.Fatal(err)
	}
	if err := conn.UploadSessionFile(ctx, file); err != nil {
		t.Fatal(err)
	}
	if err := conn.SeedMove(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	pi := conn.installDir + "/move-pi"
	if _, err := runScript(ctx, conn.Pool, strings.NewReader(evalPi), `cat > "$1" && chmod 700 "$1"`, pi); err != nil {
		t.Fatal(err)
	}
	if err := conn.CreateSession(ctx, Session{ID: "s1", Move: "m1", Cwd: feature, Args: []string{pi}}); err != nil {
		t.Fatal(err)
	}
	repositoryDestination, err := mirror.NewMovedDestination(t.TempDir(), "/", shadow)
	if err != nil {
		t.Fatal(err)
	}
	sessionDestination, err := mirror.NewDestination(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionDestination.File = file
	served := []<-chan error{serveMirror(t, conn, "m1", "", repositoryDestination), serveMirror(t, conn, "m1", file, sessionDestination)}
	attach, release, err := conn.Attach(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	input, _ := attach.StdinPipe()
	stdout, _ := attach.StdoutPipe()
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	output := bufio.NewReader(stdout)
	run := func(command string) string {
		t.Helper()
		fmt.Fprintln(input, command)
		var lines []string
		for {
			line, err := output.ReadString('\n')
			if err != nil {
				t.Fatalf("Pi ended after %q: %v", lines, err)
			}
			if line == "--\n" {
				return strings.Join(lines, "")
			}
			lines = append(lines, line)
		}
	}

	if seen, want := run(`echo "$HOME"; pwd -P; cat README; cat `+file+`; ls -A ../repo`), home+"\n"+feature+"\nmain's\n{\"type\":\"session\"}\n.git\n"; seen != want {
		t.Fatalf("the session sees\n%s\nwant\n%s", seen, want)
	}
	run(`git worktree add -q -b more ../more && echo new > ../more/new && mkdir node_modules && echo dependency > node_modules/dependency`)
	run(`echo '{"type":"message"}' >> ` + file + `; (while :; do date +%s%N > counter; done) &`)
	time.Sleep(300 * time.Millisecond)
	barriers, err := conn.ReleaseMove(ctx, "m1")
	if err != nil || barriers.Repository == 0 || barriers.Session == 0 {
		t.Fatalf("ReleaseMove = %+v, %v", barriers, err)
	}
	for _, done := range served {
		if err := <-done; err != nil {
			t.Fatalf("a mirror ended with %v", err)
		}
	}
	counter := worker.direct("cat " + ShellQuote(copy+strings.TrimPrefix(filepath.Join(feature, "counter"), home)))
	for path, want := range map[string]string{
		shadow + filepath.Join(feature, "counter"): counter, shadow + filepath.Join(private, "more", "new"): "new\n",
		file: "{\"type\":\"session\"}\n{\"type\":\"message\"}\n",
	} {
		if got, _ := os.ReadFile(path); string(got) != want {
			t.Errorf("main's %s = %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(shadow + filepath.Join(feature, "node_modules")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("main's shadow of the ignored folder: %v", err)
	}

	// The next copy of the repository takes what Git ignores from the deleted one.
	if err := conn.DeleteMove(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if left := worker.direct("ls " + ShellQuote(filepath.Dir(copy))); left != "" {
		t.Fatalf("the worker's copies after DeleteMove: %q", left)
	}
	move.ID = "m2"
	if copy, err = conn.PrepareMove(ctx, move); err != nil {
		t.Fatal(err)
	}
	if err := conn.UploadMove(ctx, "m2", shadow+home); err != nil {
		t.Fatal(err)
	}
	if err := conn.SeedMove(ctx, "m2"); err != nil {
		t.Fatal(err)
	}
	if seeded := worker.direct("cat " + ShellQuote(copy+strings.TrimPrefix(filepath.Join(feature, "node_modules", "dependency"), home))); seeded != "dependency\n" {
		t.Fatalf("the next copy's ignored folder has %q", seeded)
	}
	if _, err := conn.ReleaseMove(ctx, "m2"); err != nil {
		t.Fatal(err)
	}
	if err := conn.DeleteMove(ctx, "m2"); err != nil {
		t.Fatal(err)
	}
}
