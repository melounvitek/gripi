package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// CheckExtension starts Pi with the gateway's extension and reports why the
// commands that carry the gateway's requests are not available.
func CheckExtension(ctx context.Context, command []string, extension []byte) error {
	root, err := os.MkdirTemp("", "gripi-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	extensionPath := filepath.Join(root, "gripi-tree.ts")
	if err := os.WriteFile(extensionPath, extension, 0600); err != nil {
		return err
	}

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	process := exec.CommandContext(ctx, command[0], slices.Concat(command[1:], []string{"--mode", "rpc", "--no-session", "--extension", extensionPath})...)
	process.Dir = root
	// An empty Pi config keeps the user's own extensions and settings from failing the check.
	process.Env = append(ScrubbedEnvironment(os.Environ()), "PI_CODING_AGENT_DIR="+filepath.Join(root, "agent"), "PI_SKIP_VERSION_CHECK=1")
	process.WaitDelay = time.Second
	var stderr bytes.Buffer
	process.Stderr = &stderr
	stdin, err := process.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		return err
	}
	if err := process.Start(); err != nil {
		return fmt.Errorf("%w: %w", ErrStartFailed, err)
	}
	// Pi shuts down when its input ends, without finishing the request, so the input stays open until it answers.
	_, _ = io.WriteString(stdin, `{"id":"check","type":"get_commands"}`+"\n")
	answer, answered := commandsAnswer(stdout)
	stop()
	exit := process.Wait()
	if !answered {
		status := "Pi exited without answering"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "Pi did not answer in time"
		} else if failure := new(exec.ExitError); errors.As(exit, &failure) && failure.ExitCode() > 0 {
			status = fmt.Sprintf("Pi exited with status %d", failure.ExitCode())
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("%s:\n%s", status, detail)
		}
		return errors.New(status)
	}
	if !answer.Success {
		return fmt.Errorf("Pi could not list its commands: %s", answer.Error)
	}
	var missing []string
	for _, name := range internalCommandNames {
		if !slices.Contains(answer.Data.Commands, piCommand{name}) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Pi did not list Gripi's commands: %s", strings.Join(missing, ", "))
	}
	return nil
}

type piCommand struct {
	Name string `json:"name"`
}

type piCommands struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		Commands []piCommand `json:"commands"`
	} `json:"data"`
}

// commandsAnswer reads Pi's output until it answers the check's request.
func commandsAnswer(output io.Reader) (answer piCommands, answered bool) {
	reader := bufio.NewReader(output)
	for {
		line, err := reader.ReadBytes('\n')
		var record piCommands
		if json.Unmarshal(line, &record) == nil && record.ID == "check" && record.Type == "response" {
			return record, true
		}
		if err != nil {
			return piCommands{}, false
		}
	}
}
