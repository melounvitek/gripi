package view

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// variable tells this executable, started again by viewCommand, to enter the view, and what it needs to know for that.
const variable = "GRIPI_VIEW"

// entry is what the process that enters the view needs to know.
type entry struct {
	Home string `json:"home"` // main's home folder
	// Parent is the folder that holds main's home folder, with links resolved, which the view replaces with a copy of
	// it whose entries show the original ones, unless main's home folder is this user's.
	Parent string `json:"parent,omitempty"`
	Own    string `json:"own,omitempty"` // this user's home folder, with links resolved
	// Binds make a move's view instead: each Target shows its Source.
	Binds []binding `json:"binds,omitempty"`
	Dir   string    `json:"dir"` // the folder that the command starts in, if the view has it
	UID   int       `json:"uid"`
	GID   int       `json:"gid"`
}

type binding struct{ Source, Target string }

// viewCommand returns this executable, which enters the view of home and runs command there.
func viewCommand(home string, command []string) (*exec.Cmd, error) {
	own, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("main's home folder %q is not an absolute path", home)
	}
	home = filepath.Clean(home)
	parent, err := filepath.EvalSymlinks(filepath.Dir(home))
	if err != nil {
		return nil, fmt.Errorf("a private view needs the folder of main's home folder %s here: %w", home, err)
	}
	if own, err = filepath.EvalSymlinks(own); err != nil {
		return nil, err
	}
	dir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	view := entry{Home: home, Dir: dir, UID: os.Getuid(), GID: os.Getgid()}
	if shown := filepath.Join(parent, filepath.Base(home)); shown != own {
		if parent == "/" {
			return nil, fmt.Errorf("main's home folder %s is in /, which a private view cannot replace", home)
		}
		if within(own, shown) || within(shown, own) {
			return nil, fmt.Errorf("main's home folder %s and this user's home folder %s are inside each other, which a private view cannot show", home, own)
		}
		view.Parent, view.Own = parent, own
	}
	return startAgain(view, command, os.Environ())
}

func moveCommand(move Move, dir string, command, env []string) (*exec.Cmd, error) {
	view := entry{Home: filepath.Clean(move.Home), Dir: dir, UID: os.Getuid(), GID: os.Getgid()}
	for _, root := range move.Private {
		view.Binds = append(view.Binds, binding{Source: filepath.Join(move.Copy, filepath.Base(root)), Target: root})
	}
	if env == nil {
		env = os.Environ()
	}
	return startAgain(view, command, env)
}

// startAgain returns this executable, which enters the view and runs command there with env. Go's runtime has
// threads, which unshare(2) refuses, so the namespaces are the new process's: an unprivileged user namespace, in which
// it is root and can mount, and a mount namespace of its own.
func startAgain(view entry, command, env []string) (*exec.Cmd, error) {
	encoded, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	process := &exec.Cmd{Path: "/proc/self/exe", Args: append([]string{"gripi-view"}, command...), Env: append(slices.Clip(env), variable+"="+string(encoded))}
	process.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	return process, nil
}

func within(path, folder string) bool {
	return strings.HasPrefix(path, folder+"/")
}

// Init enters the view and runs the command there, then exits, when this process is the one that Run starts for that.
// Call it first in main.
func Init() {
	encoded, found := os.LookupEnv(variable)
	if !found {
		return
	}
	os.Unsetenv(variable)
	var entry entry
	code, err := 1, json.Unmarshal([]byte(encoded), &entry)
	if err == nil {
		code, err = entry.enter(os.Args[1:])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gripi: enter the private view of %s: %v\n", entry.Home, err)
		code = 1
	}
	os.Exit(code)
}

// enter makes the view, then runs command there as this user.
func (view entry) enter(command []string) (int, error) {
	// Nothing that the view mounts may reach the worker's other processes.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return 0, fmt.Errorf("make the mounts private: %w", err)
	}
	dir := view.Dir
	if view.Binds != nil {
		for _, bind := range view.Binds {
			if err := unix.Mount(bind.Source, bind.Target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
				return 0, fmt.Errorf("show %s at %s: %w", bind.Source, bind.Target, err)
			}
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return 0, fmt.Errorf("%s is not a folder in the move's view", dir)
		}
	} else {
		if view.Parent != "" {
			if err := view.showOwnHome(); err != nil {
				return 0, err
			}
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			dir = view.Home
		}
	}

	process := exec.Command(command[0], command[1:]...)
	process.Dir, process.Env = dir, append(os.Environ(), "HOME="+view.Home, "PWD="+dir)
	process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
	// A nested user namespace maps this user's IDs back to themselves, so the command runs as this user, not as root.
	// The command dies with this process, whose thread it follows.
	runtime.LockOSThread()
	process.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: view.UID, HostID: 0, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: view.GID, HostID: 0, Size: 1}},
		Pdeathsig:   syscall.SIGKILL,
	}
	return run(process)
}

// showOwnHome replaces the folder that holds main's home folder with a new one, where main's home folder shows this
// user's home folder and every other entry shows the original one.
func (view entry) showOwnHome() error {
	entries, err := os.ReadDir(view.Parent)
	if err != nil {
		return fmt.Errorf("list the folder of main's home folder: %w", err)
	}
	// The new folder hides the original entries, which stay reachable through the original folder's descriptor.
	original, err := unix.Open(view.Parent, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", view.Parent, err)
	}
	defer unix.Close(original)
	if err := unix.Mount("tmpfs", view.Parent, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
		return fmt.Errorf("mount an empty folder on %s: %w", view.Parent, err)
	}
	shown := filepath.Join(view.Parent, filepath.Base(view.Home))
	for _, sibling := range entries {
		path := filepath.Join(view.Parent, sibling.Name())
		if path == shown {
			continue
		}
		if err := bind(fmt.Sprintf("/proc/self/fd/%d/%s", original, sibling.Name()), path, sibling.Type()); err != nil {
			return err
		}
	}
	if err := bind(view.Own, shown, fs.ModeDir); err != nil {
		return err
	}
	// What would be made in the new folder itself would vanish with the view, so the folder is read-only.
	if err := unix.Mount("", view.Parent, "", unix.MS_REMOUNT|unix.MS_BIND|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return fmt.Errorf("make %s read-only: %w", view.Parent, err)
	}
	return nil
}

// bind makes path, in the view's new folder, show source, an entry of the given type: a link as a copy of it, and
// anything else as a bind mount, with its permissions as they are.
func bind(source, path string, kind fs.FileMode) error {
	if kind&fs.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, path)
	}
	var err error
	if kind.IsDir() {
		err = os.Mkdir(path, 0755)
	} else {
		err = os.WriteFile(path, nil, 0644)
	}
	if err != nil {
		return err
	}
	if err := unix.Mount(source, path, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
		return fmt.Errorf("show %s in the view: %w", path, err)
	}
	return nil
}
