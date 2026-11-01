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

// Init does nothing outside Linux.
func Init() {}
