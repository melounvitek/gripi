package helper_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestLeaseExpiryKillsEverySessionAtOnceBeforeMainTakesThemOver(t *testing.T) {
	const lease = 3 * time.Second
	unit := startUnitHelper(t, lease)
	unit.answering.Store(true)
	// The first session stops slowly: its Pi ignores SIGTERM, and one of its processes takes long to die.
	slow := unit.createSession("s1", "FAKE_PI_IGNORE_TERM=1")
	killed, _ := startSlowToDie(t, cgroupOf(t, slow[0]))
	s2, s3 := unit.createSession("s2"), unit.createSession("s3")
	helperPID, _ := strconv.Atoi(stat(slow[0])[1])

	unit.answering.Store(false)
	unit.answer(unit.nextPing())
	// As main does (worker.GoneMargin).
	gone := time.Now().Add(lease + 2*time.Second)

	// Shortly before the lease ends, every Pi gets SIGTERM.
	assertGone(t, lease, s2[0], s3[0])
	assertRunning(t, slow[0])
	select {
	case <-killed:
	case <-time.After(time.Until(gone)):
		t.Fatal("the slow process was not killed before main takes the sessions over")
	}
	assertGone(t, time.Until(gone), slices.Concat(slow, s2, s3)...)
	// While it waits for the slow process, the helper still reads main's requests.
	create := map[string]any{"type": "session.create", "session": "s4", "cwd": "/", "args": []string{"sleep", "1000"}}
	if response := unit.request(create); response.OK {
		t.Fatal("the helper started a session after it killed the others")
	}
	assertGone(t, time.Until(gone), helperPID)
}

func cgroupOf(t *testing.T, pid int) string {
	t.Helper()
	contents, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(strings.TrimSpace(string(contents)), "0::"))
}

// startSlowToDie starts a process in a cgroup that, once killed, stays in the cgroup until the test ends or calls die,
// as a process does that waits for a slow disk. It stops on its way out for the test, which traces it. The returned
// channel is closed when the process has been killed.
func startSlowToDie(t *testing.T, cgroup string) (killed <-chan struct{}, die func()) {
	t.Helper()
	started := make(chan error)
	stopped := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var process *os.Process
	go func() {
		defer close(finished)
		// Only the thread that started a traced process may resume it.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		folder, err := os.Open(cgroup)
		if err != nil {
			started <- err
			return
		}
		defer folder.Close()
		command := exec.Command("sleep", "1000")
		command.SysProcAttr = &syscall.SysProcAttr{Ptrace: true, UseCgroupFD: true, CgroupFD: int(folder.Fd())}
		if err := command.Start(); err != nil {
			started <- err
			return
		}
		process = command.Process
		defer process.Release()
		pid := process.Pid
		var status syscall.WaitStatus
		// It stops at exec.
		syscall.Wait4(pid, &status, 0, nil)
		err = syscall.PtraceSetOptions(pid, syscall.PTRACE_O_TRACEEXIT)
		if err == nil {
			err = syscall.PtraceCont(pid, 0)
		}
		started <- err
		if err != nil {
			syscall.Kill(pid, syscall.SIGKILL)
			syscall.Wait4(pid, &status, 0, nil)
			return
		}
		for {
			if _, err := syscall.Wait4(pid, &status, 0, nil); err != nil || !status.Stopped() {
				return
			}
			if status.TrapCause() == syscall.PTRACE_EVENT_EXIT {
				break
			}
			syscall.PtraceCont(pid, int(status.StopSignal()))
		}
		close(stopped)
		<-release
		syscall.PtraceCont(pid, 0)
		syscall.Wait4(pid, &status, 0, nil)
	}()
	if err := <-started; err != nil {
		<-finished
		t.Skipf("cannot start a traced process in the helper's cgroup: %v", err)
	}
	var once sync.Once
	die = func() {
		once.Do(func() {
			process.Kill()
			close(release)
			<-finished
		})
	}
	t.Cleanup(die)
	return stopped, die
}
