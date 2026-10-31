package helper

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// DelegatedCgroup returns the cgroup that systemd delegated to this process (systemd-run -p Delegate=yes), after
// moving the process into a leaf of it: a cgroup with child cgroups should hold no processes of its own.
func DelegatedCgroup() (string, error) {
	contents, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	var cgroup string
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if path, found := strings.CutPrefix(line, "0::"); found {
			cgroup = filepath.Join("/sys/fs/cgroup", path)
		}
	}
	if cgroup == "" {
		return "", errors.New("the helper needs cgroup v2")
	}
	if _, err := os.Stat(filepath.Join(cgroup, "cgroup.kill")); err != nil {
		return "", fmt.Errorf("the helper needs cgroup.kill, from Linux 5.14: %w", err)
	}
	processes, err := os.ReadFile(filepath.Join(cgroup, "cgroup.procs"))
	if err != nil {
		return "", err
	}
	// Outside its own unit, the helper would take over a cgroup that other processes share.
	if strings.TrimSpace(string(processes)) != strconv.Itoa(os.Getpid()) {
		return "", errors.New("the helper must run alone in a cgroup that systemd delegated to it (systemd-run --user -p Delegate=yes)")
	}
	leaf := filepath.Join(cgroup, "helper")
	if err := os.Mkdir(leaf, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte("0"), 0); err != nil {
		return "", fmt.Errorf("move the helper into %s: %w", leaf, err)
	}
	return cgroup, nil
}

// startInCgroup starts command inside cgroup, which it creates if needed. CLONE_INTO_CGROUP leaves no moment in which
// the process or its children could start outside it.
func startInCgroup(command *exec.Cmd, cgroup string) error {
	if err := os.Mkdir(cgroup, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	descriptor, err := unix.Open(cgroup, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(descriptor)
	command.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: descriptor}
	return command.Start()
}

// killCgroup kills every process in cgroup, including those in sessions of their own, and removes it.
func killCgroup(cgroup string) error {
	err := os.WriteFile(filepath.Join(cgroup, "cgroup.kill"), []byte("1"), 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		events, err := os.ReadFile(filepath.Join(cgroup, "cgroup.events"))
		if err != nil {
			return err
		}
		if strings.Contains(string(events), "populated 0") {
			return os.Remove(cgroup)
		}
	}
	return fmt.Errorf("%s still has processes after cgroup.kill", cgroup)
}

// bootTime is CLOCK_BOOTTIME, which keeps counting while the machine is suspended, unlike Go's monotonic clock.
func bootTime() time.Duration {
	var now unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &now); err != nil {
		panic(err)
	}
	return time.Duration(now.Nano())
}
