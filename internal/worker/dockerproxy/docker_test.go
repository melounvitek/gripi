package dockerproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
)

// TestRealDocker runs the docker CLI and docker compose through the proxy against the real Docker daemon. Main's home
// path holds another user's files here, so containers find the worker's files only if the proxy maps the paths. The
// proxy isn't in a view here, so the test names symbolic links in the worker's home by the path that the view also
// shows them at.
func TestRealDocker(t *testing.T) {
	if os.Getenv("GRIPI_TEST_DOCKER") == "" {
		t.Skip("set GRIPI_TEST_DOCKER=1 to run the docker CLI through the proxy against this machine's Docker")
	}
	if output, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker doesn't work: %v\n%s", err, output)
	}
	if exec.Command("docker", "image", "inspect", "alpine").Run() != nil {
		if output, err := exec.Command("docker", "pull", "alpine").CombinedOutput(); err != nil {
			t.Skipf("can't pull alpine: %v\n%s", err, output)
		}
		t.Cleanup(func() { exec.Command("docker", "image", "rm", "alpine").Run() })
	}
	dir := t.TempDir()
	session := filepath.Join(dir, "home", "vitek")
	worker := filepath.Join(dir, "worker")
	image := filepath.Join(dir, "image")
	for path, contents := range map[string]string{
		filepath.Join(session, "proj", "file"):           "another user's file\n",
		filepath.Join(session, "proj", "shared", "file"): "another user's file\n",
		filepath.Join(worker, "proj", "file"):            "hello from the worker\n",
		filepath.Join(worker, "proj", "shared", "file"):  "shared on the worker\n",
		filepath.Join(image, "Dockerfile"):               "FROM alpine\nCOPY file /file\n",
		filepath.Join(image, "file"):                     "built\n",
		filepath.Join(worker, "compose.yaml"):            "services:\n  app:\n    build: " + image + "\n    volumes:\n      - ./proj:/x\n    command: cat /x/file /file\n",
	} {
		os.MkdirAll(filepath.Dir(path), 0700)
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// In the view, the link leads to the worker's own shared folder.
	link := filepath.Join(worker, "proj", "link")
	if err := os.Symlink(filepath.Join(session, "proj", "shared"), link); err != nil {
		t.Fatal(err)
	}
	otherUser := tree(t, session)
	client, socket := serve(t, dockerproxy.Options{
		Listen: filepath.Join(dir, "docker.sock"), Upstream: dockerproxy.Upstream(os.Environ()), Mappings: []dockerproxy.Mapping{{From: session, To: worker}},
	})
	name := "gripi-dockerproxy-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	// Cleanup goes to the daemon directly, so it works even when the proxy doesn't.
	cleanup := func(args ...string) { t.Cleanup(func() { exec.Command("docker", args...).Run() }) }
	prepare := func(stdin io.Reader, program string, args ...string) *exec.Cmd {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		t.Cleanup(cancel)
		command := exec.CommandContext(ctx, program, args...)
		command.Env = append(os.Environ(), "DOCKER_HOST=unix://"+socket)
		command.Stdin = stdin
		return command
	}
	run := func(t *testing.T, stdin io.Reader, program string, args ...string) string {
		t.Helper()
		var stderr bytes.Buffer
		command := prepare(stdin, program, args...)
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s%s", command, err, output, &stderr)
		}
		return string(output)
	}
	docker := func(t *testing.T, args ...string) string {
		t.Helper()
		return run(t, nil, "docker", args...)
	}
	expect := func(t *testing.T, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}

	// A wrong path makes --mount fail, where binds would make the daemon create a folder owned by root, so this must
	// pass first.
	if !t.Run("mount", func(t *testing.T) {
		expect(t, docker(t, "run", "--rm", "--mount", "type=bind,source="+session+"/proj,target=/x", "alpine", "cat", "/x/file"), "hello from the worker\n")
	}) {
		return
	}
	t.Run("volume flag and inspect", func(t *testing.T) {
		expect(t, docker(t, "run", "--rm", "-v", session+"/proj:/x:ro", "alpine", "cat", "/x/file"), "hello from the worker\n")
		cleanup("rm", "-f", name+"-inspect")
		docker(t, "create", "--name", name+"-inspect", "-v", session+"/proj:/x", "alpine")
		expect(t, docker(t, "inspect", "-f", "{{json .HostConfig.Binds}} {{range .Mounts}}{{.Source}}{{end}}", name+"-inspect"),
			`["`+session+`/proj:/x"] `+session+"/proj\n")
	})
	t.Run("local volume bound to a folder", func(t *testing.T) {
		cleanup("volume", "rm", "-f", name)
		docker(t, "volume", "create", "-o", "type=none", "-o", "o=bind", "-o", "device="+session+"/proj", name)
		expect(t, docker(t, "run", "--rm", "-v", name+":/x", "alpine", "cat", "/x/file"), "hello from the worker\n")
		expect(t, docker(t, "volume", "inspect", "-f", "{{.Options.device}}", name), session+"/proj\n")
	})
	t.Run("stdin", func(t *testing.T) {
		expect(t, run(t, strings.NewReader("piped\n"), "docker", "run", "-i", "--rm", "alpine", "sh", "-c", "cat; echo done"), "piped\ndone\n")
	})
	t.Run("terminal", func(t *testing.T) {
		if _, err := exec.LookPath("script"); err != nil {
			t.Skip("needs script for a terminal")
		}
		if output := run(t, nil, "script", "-qec", "docker run -it --rm alpine sh -c 'test -t 0 && echo terminal'", "/dev/null"); !strings.Contains(output, "terminal\r\n") {
			t.Errorf("got %q", output)
		}
	})
	t.Run("exec", func(t *testing.T) {
		cleanup("rm", "-f", name+"-exec")
		docker(t, "run", "-d", "--name", name+"-exec", "-v", session+"/proj:/x", "alpine", "sleep", "300")
		expect(t, docker(t, "exec", name+"-exec", "cat", "/x/file"), "hello from the worker\n")
		expect(t, run(t, strings.NewReader("piped\n"), "docker", "exec", "-i", name+"-exec", "sh", "-c", "cat; echo done"), "piped\ndone\n")
	})
	t.Run("logs follow", func(t *testing.T) {
		cleanup("rm", "-f", name+"-logs")
		docker(t, "run", "-d", "--name", name+"-logs", "alpine", "sh", "-c", "echo one; sleep 3; echo two")
		logs := prepare(nil, "docker", "logs", "-f", name+"-logs")
		stdout, _ := logs.StdoutPipe()
		if err := logs.Start(); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(stdout)
		line, _ := reader.ReadString('\n')
		expect(t, line, "one\n")
		expect(t, docker(t, "inspect", "-f", "{{.State.Running}}", name+"-logs"), "true\n")
		rest, _ := io.ReadAll(reader)
		expect(t, string(rest), "two\n")
		if err := logs.Wait(); err != nil {
			t.Errorf("docker logs -f: %v", err)
		}
	})
	t.Run("build", func(t *testing.T) {
		tag := name + ":latest"
		cleanup("image", "rm", "-f", tag)
		docker(t, "build", "-q", "-t", tag, image)
		expect(t, docker(t, "run", "--rm", tag, "cat", "/file"), "built\n")
	})
	t.Run("compose", func(t *testing.T) {
		compose := []string{"compose", "-p", name, "-f", filepath.Join(worker, "compose.yaml"), "--project-directory", session}
		cleanup(append(compose, "down", "--rmi", "local")...)
		output := docker(t, append(compose, "up", "--build", "--abort-on-container-exit")...)
		if !strings.Contains(output, "hello from the worker") || !strings.Contains(output, "built") {
			t.Errorf("got %q", output)
		}
	})
	t.Run("symbolic links and dot segments", func(t *testing.T) {
		expect(t, docker(t, "run", "--rm", "--mount", "type=bind,source="+link+",target=/x", "alpine", "cat", "/x/file"), "shared on the worker\n")
		expect(t, docker(t, "run", "--rm", "--mount", "type=bind,source="+dir+"/home/./vitek/proj,target=/x", "alpine", "cat", "/x/file"), "hello from the worker\n")
		expect(t, docker(t, "run", "--rm", "-v", link+":/x:ro", "alpine", "cat", "/x/file"), "shared on the worker\n")
		cleanup("volume", "rm", "-f", name+"-link")
		docker(t, "volume", "create", "-o", "type=none", "-o", "o=bind", "-o", "device="+link, name+"-link")
		expect(t, docker(t, "run", "--rm", "-v", name+"-link:/x", "alpine", "cat", "/x/file"), "shared on the worker\n")
	})
	t.Run("lowercase fields", func(t *testing.T) {
		cleanup("rm", "-f", name+"-lowercase")
		body := `{"Image": "alpine", "Cmd": ["cat", "/x/file"], "hostconfig": {"binds": ["` + session + `/proj:/x:ro"]}}`
		res, err := client.Post("http://docker/containers/create?name="+name+"-lowercase", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		answer, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("creating the container: %s %s", res.Status, answer)
		}
		expect(t, docker(t, "start", "-a", name+"-lowercase"), "hello from the worker\n")
	})
	if got := tree(t, session); !reflect.DeepEqual(got, otherUser) {
		t.Errorf("the other user's folder changed from %v to %v", otherUser, got)
	}
}

// tree returns the contents of the files and folders under dir, by path.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			files[path] = "folder"
			return err
		}
		contents, err := os.ReadFile(path)
		files[path] = string(contents)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
