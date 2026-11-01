//go:build !linux

package helper

import (
	"errors"
	"os/exec"
	"time"
)

var errLinuxOnly = errors.New("the worker helper needs Linux")

func DelegatedCgroup() (string, error) { return "", errLinuxOnly }

func startInCgroup(*exec.Cmd, string) error { return errLinuxOnly }

func killCgroups(time.Duration, ...string) error { return nil }

var started = time.Now()

func bootTime() time.Duration { return time.Since(started) }
