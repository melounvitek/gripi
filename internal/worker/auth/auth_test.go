package auth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakePiScript stands in for Pi's auth commands. It logs its agent folder and arguments next to itself, and prints
// the answer file named after the provider, or fails as Pi does.
const fakePiScript = `printf '%s %s\n' "$PI_CODING_AGENT_DIR" "$*" >> "$0.log"
answer="$(dirname "$0")/answers/$4"
if [ "$1" = auth ] && [ -f "$answer" ]; then
	printf '%s\n' "$(cat "$answer")"
	exit 0
fi
echo "Error: Unknown provider \"$4\". Use --list-models to see available providers." >&2
exit 1
`

// fakePi returns main's Pi as a script, run like Gripi's configured Pi command, with an empty agent folder. The
// script answers answers[provider] for that provider. calls returns the agent folder and arguments of each run.
func fakePi(t *testing.T, answers map[string]string) (pi Pi, calls func() []string) {
	t.Helper()
	directory := t.TempDir()
	script := filepath.Join(directory, "pi")
	writeFile(t, script, fakePiScript)
	for provider, answer := range answers {
		writeFile(t, filepath.Join(directory, "answers", provider), answer)
	}
	pi = Pi{Command: []string{"/bin/sh", script, "--approve"}, AgentDir: t.TempDir()}
	return pi, func() []string {
		log, err := os.ReadFile(script + ".log")
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(log)), "\n")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func decode(t *testing.T, contents []byte) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(contents, &decoded); err != nil {
		t.Fatalf("worker's auth.json %s: %v", contents, err)
	}
	return decoded
}

func TestBuildGivesTheWorkerKeysAndAccessTokensButNoRefreshTokens(t *testing.T) {
	pi, calls := fakePi(t, map[string]string{"anthropic": "!sk-ant-$1"})
	writeFile(t, filepath.Join(pi.AgentDir, "auth.json"), `{
  "openai": {"type": "api_key", "key": "sk-openai", "env": {"OPENAI_ORG_ID": "org"}},
  "groq": {"type": "api_key", "key": "$GROQ_KEY"},
  "anthropic": {"type": "api_key", "key": "!pass show anthropic"},
  "openai-codex": {"type": "oauth", "access": "access-codex", "refresh": "refresh-codex", "expires": 1793457725000, "accountId": "account"},
  "github-copilot": {"type": "oauth", "access": "access-copilot", "refresh": "refresh-copilot", "expires": 1793450000000, "enterpriseUrl": "ghe.example.com"}
}`)
	// Pi uses auth.json's key over models.json's, so the command here never runs.
	writeFile(t, filepath.Join(pi.AgentDir, "models.json"), `{"providers": {"openai": {"apiKey": "!pass show openai"}}}`)

	contents, expires, err := pi.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{
		"openai": map[string]any{"type": "api_key", "key": "sk-openai", "env": map[string]any{"OPENAI_ORG_ID": "org"}},
		"groq":   map[string]any{"type": "api_key", "key": "$GROQ_KEY"},
		// Escaped, so that Pi reads the key as it is rather than as a command or a variable.
		"anthropic": map[string]any{"type": "api_key", "key": "$!sk-ant-$$1"},
		"openai-codex": map[string]any{
			"type": "oauth", "access": "access-codex", "refresh": "gripi:refresh-on-main", "expires": 1793457725000.0, "accountId": "account",
		},
		"github-copilot": map[string]any{
			"type": "oauth", "access": "access-copilot", "refresh": "gripi:refresh-on-main", "expires": 1793450000000.0, "enterpriseUrl": "ghe.example.com",
		},
	}
	if got := decode(t, contents); !reflect.DeepEqual(got, want) {
		t.Fatalf("worker's auth.json = %#v, want %#v", got, want)
	}
	wantExpires := map[string]time.Time{
		"openai-codex":   time.UnixMilli(1793457725000),
		"github-copilot": time.UnixMilli(1793450000000),
	}
	if !reflect.DeepEqual(expires, wantExpires) {
		t.Fatalf("expires = %v, want %v", expires, wantExpires)
	}
	if got, want := calls(), []string{pi.AgentDir + " auth print-api-key --provider anthropic"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pi ran %q, want %q", got, want)
	}
}

func TestBuildResolvesCommandKeysFromModelsJSON(t *testing.T) {
	pi, calls := fakePi(t, map[string]string{"cli-proxy-api": "sk-proxy"})
	// Pi reads models.json with comments and trailing commas.
	writeFile(t, filepath.Join(pi.AgentDir, "models.json"), `{
  // A local proxy, reached from the worker through a forwarded port.
  "providers": {
    "cli-proxy-api": {
      "baseUrl": "http://127.0.0.1:8317/v1",
      "apiKey": "!/usr/bin/head -1 ~/.config/cliproxyapi/client-key",
      "models": [{"id": "gpt-6.1-sol"},],
    },
    "literal": {"baseUrl": "http://127.0.0.1:1/v1", "apiKey": "sk-literal"},
    "variable": {"baseUrl": "http://127.0.0.1:1/v1", "apiKey": "$PROXY_KEY"},
    "keyless": {"baseUrl": "http://127.0.0.1:1/v1"},
  },
}`)

	contents, expires, err := pi.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"cli-proxy-api": map[string]any{"type": "api_key", "key": "sk-proxy"}}
	if got := decode(t, contents); !reflect.DeepEqual(got, want) {
		t.Fatalf("worker's auth.json = %#v, want %#v", got, want)
	}
	if len(expires) != 0 {
		t.Fatalf("expires = %v, want none", expires)
	}
	if got, want := calls(), []string{pi.AgentDir + " auth print-api-key --provider cli-proxy-api"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Pi ran %q, want %q", got, want)
	}
}

func TestBuildFailsWhenMainCannotResolveACommandKey(t *testing.T) {
	pi, _ := fakePi(t, nil)
	writeFile(t, filepath.Join(pi.AgentDir, "auth.json"), `{"anthropic": {"type": "api_key", "key": "!false"}}`)

	_, _, err := pi.Build(t.Context())

	if err == nil || !strings.Contains(err.Error(), `Error: Unknown provider "anthropic"`) {
		t.Fatalf("Build = %v, want Pi's error", err)
	}
}

func TestBuildRetriesWhilePiRewritesAuthJSON(t *testing.T) {
	pi, _ := fakePi(t, nil)
	path := filepath.Join(pi.AgentDir, "auth.json")
	// Pi truncates auth.json and writes it again in place, so a reader can catch it half-written.
	writeFile(t, path, `{"openai": {"type": "api_`)
	written := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		written <- os.WriteFile(path, []byte(`{"openai": {"type": "api_key", "key": "sk-openai"}}`), 0o600)
	}()

	contents, _, err := pi.Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}

	want := map[string]any{"openai": map[string]any{"type": "api_key", "key": "sk-openai"}}
	if got := decode(t, contents); !reflect.DeepEqual(got, want) {
		t.Fatalf("worker's auth.json = %#v, want %#v", got, want)
	}
}

func TestBuildFailsWhenAuthJSONStaysUnparsable(t *testing.T) {
	pi, _ := fakePi(t, nil)
	writeFile(t, filepath.Join(pi.AgentDir, "auth.json"), `{"openai": `)

	if _, _, err := pi.Build(t.Context()); err == nil || !strings.Contains(err.Error(), "auth.json") {
		t.Fatalf("Build = %v, want an error about auth.json", err)
	}
}

func TestRefreshSchedule(t *testing.T) {
	seen := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		expires   time.Time
		lead      time.Duration
		minExpiry string
	}{
		// Refreshed 15 minutes before expiry; the new token must last longer than what is left.
		{"hour-long token", seen.Add(55 * time.Minute), 15 * time.Minute, "960s"},
		// GitHub Copilot's tokens last about 25 minutes, and Pi saves them as expiring 5 minutes early.
		{"Copilot token", seen.Add(20 * time.Minute), 10 * time.Minute, "660s"},
		// Gripi saw it late: refreshed at once, still before the worker's Pi would try in its last 5 minutes.
		{"token seen near its expiry", seen.Add(6 * time.Minute), 7 * time.Minute, "480s"},
		// OpenRouter's never expires.
		{"token that never expires", time.UnixMilli(9007199254740991), 15 * time.Minute, "960s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pi, calls := fakePi(t, map[string]string{"github-copilot": "token"})

			if got, want := RefreshAt(seen, test.expires), test.expires.Add(-test.lead); !got.Equal(want) {
				t.Fatalf("RefreshAt = %v, want %v", got, want)
			}
			if err := pi.Refresh("github-copilot", seen, test.expires); err != nil {
				t.Fatal(err)
			}
			want := []string{pi.AgentDir + " auth print-bearer-token --provider github-copilot --min-expiry " + test.minExpiry}
			if got := calls(); !reflect.DeepEqual(got, want) {
				t.Fatalf("Pi ran %q, want %q", got, want)
			}
		})
	}
}

func TestRefreshReportsPisError(t *testing.T) {
	pi, _ := fakePi(t, nil)
	now := time.Now()

	err := pi.Refresh("anthropic", now, now.Add(time.Hour))

	if err == nil || !strings.Contains(err.Error(), `Error: Unknown provider "anthropic"`) {
		t.Fatalf("Refresh = %v, want Pi's error", err)
	}
}
