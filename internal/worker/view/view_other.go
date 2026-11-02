//go:build !linux

package view

import (
	"errors"
	"fmt"
	"os/exec"
)

func viewCommand(string, []string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("a private view needs Linux: %w", errors.ErrUnsupported)
}

func moveCommand(Move, string, []string, []string) (*exec.Cmd, error) {
	return viewCommand("", nil)
}

// Init does nothing outside Linux.
func Init() {}
