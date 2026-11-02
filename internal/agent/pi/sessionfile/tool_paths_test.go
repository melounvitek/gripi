package sessionfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestToolPathsAreThePathsThatTheToolCallsNamed(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session.jsonl")
	call := func(name string, arguments map[string]string) string {
		line, _ := json.Marshal(map[string]any{"type": "message", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "cd /not/a/tool/call"},
			map[string]any{"type": "toolCall", "id": "1", "name": name, "arguments": arguments},
		}}})
		return string(line)
	}
	lines := []string{
		`{"type":"session","cwd":"/home/me/Work/app"}`,
		call("read", map[string]string{"path": "README.md"}),
		call("edit", map[string]string{"path": "/home/me/Work/app/src/main.go"}),
		call("write", map[string]string{"path": "~/notes.txt"}),
		call("bash", map[string]string{"command": `cd ../.worktrees/app/feature && git -C "/home/me/Work/other repo" status; ls lib && cat '/home/me/Work/app-docs/a b.md' --file=/etc/hosts -o ./out|wc -l>~/count`}),
		call("bash", map[string]string{"command": "git worktree add -b x ../wt && make -C sub"}),
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"read /home/me/secret"}]}}`,
		`not json`,
	}
	contents := ""
	for _, line := range lines {
		contents += line + "\n"
	}
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	paths, err := Store{}.ToolPaths(file, "/home/me/Work/app", "/home/me")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/home/me/Work/app/README.md", "/home/me/Work/app/src/main.go", "/home/me/notes.txt",
		"/home/me/Work/.worktrees/app/feature", "/home/me/Work/other repo", "/home/me/Work/app-docs/a b.md", "/etc/hosts", "/home/me/count",
		"/home/me/Work/app/sub",
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("ToolPaths =\n%q\nwant\n%q", paths, want)
	}

	if paths, err := (Store{}).ToolPaths(filepath.Join(t.TempDir(), "unsaved.jsonl"), "/home/me/Work/app", "/home/me"); err != nil || len(paths) != 0 {
		t.Fatalf("ToolPaths of an unsaved session = %q, %v", paths, err)
	}
}
