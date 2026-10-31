package auth

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installedPi returns the installed pi and the file of its package that stores logins, or skips the test. The test
// gets a temporary home, so that Pi can't touch the user's.
func installedPi(t *testing.T) (command, authStorage string) {
	t.Helper()
	command, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi is not installed")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	script, err := filepath.EvalSymlinks(command)
	if err != nil {
		t.Fatal(err)
	}
	for directory := filepath.Dir(script); directory != filepath.Dir(directory); directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "package.json")); err == nil {
			authStorage = filepath.Join(directory, "dist", "core", "auth-storage.js")
			break
		}
	}
	if _, err := os.Stat(authStorage); authStorage == "" || err != nil {
		t.Skipf("Pi's auth storage not found next to %s", script)
	}
	t.Setenv("HOME", t.TempDir())
	return command, authStorage
}

func TestBuiltAuthJSONGivesTheWorkersPiMainsKeys(t *testing.T) {
	command, _ := installedPi(t)
	secrets := t.TempDir()
	writeFile(t, filepath.Join(secrets, "anthropic"), "!sk-ant-$1")
	writeFile(t, filepath.Join(secrets, "proxy"), "sk-proxy")
	main := Pi{Command: []string{command, "--approve"}, AgentDir: t.TempDir()}
	writeFile(t, filepath.Join(main.AgentDir, "auth.json"), `{"anthropic": {"type": "api_key", "key": "!cat `+secrets+`/anthropic"}}`)
	models := `{"providers": {"proxy": {
  "baseUrl": "http://127.0.0.1:1/v1", "api": "openai-completions", "apiKey": "!cat ` + secrets + `/proxy", "models": [{"id": "model"}]
}}}`
	writeFile(t, filepath.Join(main.AgentDir, "models.json"), models)

	contents, _, err := main.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	worker := t.TempDir()
	writeFile(t, filepath.Join(worker, "auth.json"), string(contents))
	writeFile(t, filepath.Join(worker, "models.json"), models)
	// Only main has the secrets, so the worker's Pi must not run the commands.
	if err := os.RemoveAll(secrets); err != nil {
		t.Fatal(err)
	}
	for provider, want := range map[string]string{"anthropic": "!sk-ant-$1", "proxy": "sk-proxy"} {
		pi := exec.CommandContext(t.Context(), command, "auth", "print-api-key", "--provider", provider)
		pi.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+worker)
		key, err := pi.Output()
		if err != nil {
			t.Fatalf("worker's Pi for %s: %v", provider, err)
		}
		if got := strings.TrimSuffix(string(key), "\n"); got != want {
			t.Fatalf("worker's Pi uses %q for %s, want %q", got, provider, want)
		}
	}
}

// piReader is a long-running Pi on the worker. It holds the lock on auth.json for a while, as a refresh does, and
// then saves a login. Then it reads the file through Pi's own storage until its input closes, while a reader that
// ignores the lock counts the times it found the file half-written.
const piReader = `import { readFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";

const [authStorage, path] = process.argv.slice(2);
const { AuthStorage } = await import(authStorage);
const storage = AuthStorage.create(path);
await storage.modify("pi", async () => {
	console.log("locked");
	await sleep(300);
	return { type: "api_key", key: "saved by Pi" };
});
console.log("saved");

let open = true;
process.stdin.on("end", () => (open = false)).resume();
let reads = 0;
let torn = 0;
while (open) {
	try {
		JSON.parse(readFileSync(path, "utf8"));
	} catch {
		torn++;
	}
	await storage.read("gripi");
	reads++;
	await new Promise(setImmediate);
}
console.log(JSON.stringify({ reads, torn, key: (await storage.read("gripi"))?.key }));
`

func TestWriteLockedWithARunningPi(t *testing.T) {
	_, authStorage := installedPi(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "auth.json")
	writeFile(t, path, "{}")
	script := filepath.Join(directory, "reader.mjs")
	writeFile(t, script, piReader)
	pi := exec.CommandContext(t.Context(), "node", script, authStorage, path)
	input, err := pi.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := pi.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	pi.Stderr = &stderr
	if err := pi.Start(); err != nil {
		t.Fatal(err)
	}
	defer pi.Wait()
	defer input.Close()
	lines := bufio.NewScanner(output)
	if !lines.Scan() || lines.Text() != "locked" {
		t.Fatalf("Pi didn't take the lock: %q\n%s", lines.Text(), stderr.String())
	}

	// Sizes vary, so that a reader would notice a half-written file.
	auth := func(n int) []byte {
		return fmt.Appendf(nil, `{"gripi": {"type": "api_key", "key": "%d"}, "padding": {"type": "api_key", "key": "%s"}}`, n, strings.Repeat("x", n%50*100))
	}
	if err := WriteLocked(t.Context(), path, auth(0)); err != nil {
		t.Fatal(err)
	}
	if !lines.Scan() || lines.Text() != "saved" {
		t.Fatalf("Pi didn't save its login: %q\n%s", lines.Text(), stderr.String())
	}
	// Written after Pi saved its login under the lock, so not overwritten by it.
	assertContents(t, path, string(auth(0)))

	n := 0
	for start := time.Now(); time.Since(start) < 500*time.Millisecond; {
		n++
		if err := WriteLocked(t.Context(), path, auth(n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if !lines.Scan() {
		t.Fatalf("Pi didn't report its reads\n%s", stderr.String())
	}
	var report struct {
		Reads, Torn int
		Key         string
	}
	if err := json.Unmarshal(lines.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Reads == 0 || report.Torn != 0 || report.Key != strconv.Itoa(n) {
		t.Fatalf("after %d writes, Pi read %+v, want no torn reads and the last key", n, report)
	}
}
