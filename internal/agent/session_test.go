package agent

import "testing"

func TestNotificationPreviewPreservesMarkdownLineBoundaries(t *testing.T) {
	value := "```js\r\n  const branch = 'feat/my_branch_name';  \r\n```"
	want := "```js\nconst branch = 'feat/my_branch_name';\n```"

	if got := NotificationPreview(value); got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestDisplayHomePathReplacesOnlyWholeHomePaths(t *testing.T) {
	home := "/home/alice"
	for value, want := range map[string]string{
		"/home/alice":                 "~",
		"cd /home/alice/src && ls":    "cd ~/src && ls",
		"/home/alice:/home/alice/bin": "~:~/bin",
		"/home/alicex/file":           "/home/alicex/file",
		"/prefix/home/alice/kept.go":  "/prefix/home/alice/kept.go",
	} {
		if got := DisplayHomePath(value, home); got != want {
			t.Errorf("DisplayHomePath(%q) = %q, want %q", value, got, want)
		}
	}
	if got := DisplayHomePath("/home/alice", ""); got != "/home/alice" {
		t.Errorf("DisplayHomePath without home = %q", got)
	}
}
