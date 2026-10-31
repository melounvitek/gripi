// Package auth gives a worker main's logins without refresh tokens, so that only main's Pi refreshes them: OpenAI's
// and Anthropic's refresh tokens work once, and two machines refreshing one login revoke it on both.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/melounvitek/gripi/internal/agent"
)

const refreshPlaceholder = "gripi:refresh-on-main"

const (
	// Pi refreshes a login whose token expires within 5 minutes; on a worker that fails.
	piRefreshWindow = 5 * time.Minute
	// pushTime is enough to refresh a login on main and push it to the worker.
	pushTime = 2 * time.Minute
)

// Pi runs main's Pi.
type Pi struct {
	// Command is Gripi's Pi command, such as node and a script. Options at its end, such as --approve, are left out,
	// because Pi takes auth commands only as its first arguments.
	Command []string
	// AgentDir is main's Pi agent folder.
	AgentDir string
}

type entry = map[string]json.RawMessage

// Build returns the worker's auth.json, made from main's auth.json and models.json, and when each OAuth login in it
// expires. OAuth logins keep everything but their refresh token. API keys stay as they are, except keys that Pi gets
// by running a command: main resolves those, because the commands may read files only main has.
func (pi Pi) Build(ctx context.Context) (contents []byte, expires map[string]time.Time, err error) {
	entries, err := readAuth(ctx, filepath.Join(pi.AgentDir, "auth.json"))
	if err != nil {
		return nil, nil, err
	}
	expires = map[string]time.Time{}
	for _, provider := range slices.Sorted(maps.Keys(entries)) {
		credential := entries[provider]
		var kind string
		_ = json.Unmarshal(credential["type"], &kind)
		switch kind {
		case "api_key":
			var key string
			_ = json.Unmarshal(credential["key"], &key)
			if strings.HasPrefix(key, "!") {
				if credential["key"], err = pi.apiKey(ctx, provider); err != nil {
					return nil, nil, err
				}
			}
		case "oauth":
			var milliseconds float64
			_ = json.Unmarshal(credential["expires"], &milliseconds)
			expires[provider] = time.UnixMilli(int64(milliseconds))
			credential["refresh"], _ = json.Marshal(refreshPlaceholder)
		}
	}

	models, err := readModels(filepath.Join(pi.AgentDir, "models.json"))
	if err != nil {
		return nil, nil, err
	}
	for _, provider := range slices.Sorted(maps.Keys(models.Providers)) {
		// Pi uses a key saved in auth.json rather than models.json's.
		if _, saved := entries[provider]; saved || !strings.HasPrefix(models.Providers[provider].APIKey, "!") {
			continue
		}
		key, err := pi.apiKey(ctx, provider)
		if err != nil {
			return nil, nil, err
		}
		entries[provider] = entry{"type": json.RawMessage(`"api_key"`), "key": key}
	}

	contents, err = json.MarshalIndent(entries, "", "  ")
	return contents, expires, err
}

// apiKey returns provider's API key, resolved by main's Pi, as a JSON string in auth.json.
func (pi Pi) apiKey(ctx context.Context, provider string) (json.RawMessage, error) {
	var output strings.Builder
	if err := pi.run(ctx, &output, "auth", "print-api-key", "--provider", provider); err != nil {
		return nil, err
	}
	// Pi reads $ and a leading ! in a saved key as a variable or a command, unless escaped.
	key := strings.NewReplacer("$", "$$", "!", "$!").Replace(strings.TrimSuffix(output.String(), "\n"))
	return json.Marshal(key)
}

// RefreshAt returns when main should refresh an OAuth login that expires at expires and that Gripi first saw at
// seen, which tells how long the provider's tokens last.
func RefreshAt(seen, expires time.Time) time.Time {
	return expires.Add(-lead(seen, expires))
}

// lead is how long before its expiry main refreshes a login: 15 minutes, or half its life for short-lived tokens
// such as GitHub Copilot's, but early enough to push the new token before the worker's Pi tries to refresh it.
func lead(seen, expires time.Time) time.Duration {
	return max(min(15*time.Minute, expires.Sub(seen)/2), piRefreshWindow+pushTime)
}

// Refresh makes main's Pi refresh provider's OAuth login, under Pi's own lock, when RefreshAt says it is due. Pi
// saves the new token to main's auth.json and prints it; the printed token is discarded.
func (pi Pi) Refresh(ctx context.Context, provider string, seen, expires time.Time) error {
	// Pi refreshes a token that expires within --min-expiry, and fails if the new one does too. A minute more than
	// is left when due forces a refresh and stays below a new token's life.
	minExpiry := lead(seen, expires) + time.Minute
	return pi.run(ctx, io.Discard, "auth", "print-bearer-token", "--provider", provider, "--min-expiry", fmt.Sprintf("%ds", minExpiry/time.Second))
}

func (pi Pi) run(ctx context.Context, stdout io.Writer, args ...string) error {
	command := pi.Command
	if option := slices.IndexFunc(command[1:], func(arg string) bool { return strings.HasPrefix(arg, "-") }); option >= 0 {
		command = command[:option+1]
	}
	process := exec.CommandContext(ctx, command[0], slices.Concat(command[1:], args)...)
	process.Env = append(agent.ScrubbedEnvironment(os.Environ()), "PI_CODING_AGENT_DIR="+pi.AgentDir)
	process.Stdout = stdout
	var stderr strings.Builder
	process.Stderr = &stderr
	if err := process.Run(); err != nil {
		return fmt.Errorf("pi %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

const (
	readAttempts = 50
	readRetry    = 20 * time.Millisecond
)

var byteOrderMark = []byte("\ufeff")

// readAuth reads main's auth.json without Pi's lock, so as never to write in main's agent folder. Pi rewrites the
// file in place, so a read can catch it empty or half-written, and is retried.
func readAuth(ctx context.Context, path string) (map[string]entry, error) {
	for attempt := 1; ; attempt++ {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]entry{}, nil
		}
		if err != nil {
			return nil, err
		}
		entries := map[string]entry{}
		err = json.Unmarshal(bytes.TrimPrefix(data, byteOrderMark), &entries)
		if err == nil {
			return entries, nil
		}
		if attempt == readAttempts {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(readRetry):
		}
	}
}

type modelsFile struct {
	Providers map[string]struct {
		APIKey string `json:"apiKey"`
	}
}

// Like Pi, models.json may have // comments and trailing commas.
var (
	jsonComment   = regexp.MustCompile(`"(?:\\.|[^"\\])*"|//[^\n]*`)
	trailingComma = regexp.MustCompile(`"(?:\\.|[^"\\])*"|,\s*[}\]]`)
)

func readModels(path string) (modelsFile, error) {
	var models modelsFile
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return models, nil
	}
	if err != nil {
		return models, err
	}
	text := jsonComment.ReplaceAllStringFunc(string(bytes.TrimPrefix(data, byteOrderMark)), func(match string) string {
		if match[0] == '"' {
			return match
		}
		return ""
	})
	text = trailingComma.ReplaceAllStringFunc(text, func(match string) string {
		if match[0] == '"' {
			return match
		}
		return match[1:]
	})
	if err := json.Unmarshal([]byte(text), &models); err != nil {
		return models, fmt.Errorf("%s: %w", path, err)
	}
	return models, nil
}
