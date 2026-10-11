package rpc

import (
	"errors"
	"os/exec"
	"syscall"
)

type processGroup interface {
	Signal(bool) error
	Close() error
}

type unixProcessGroup struct{ pid int }

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func attachProcessGroup(command *exec.Cmd) (processGroup, error) {
	return &unixProcessGroup{pid: command.Process.Pid}, nil
}

func (group *unixProcessGroup) Signal(kill bool) error {
	signal := syscall.SIGTERM
	if kill {
		signal = syscall.SIGKILL
	}
	err := syscall.Kill(-group.pid, signal)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

func (group *unixProcessGroup) Close() error { return group.Signal(true) }
