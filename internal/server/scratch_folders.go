package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// scratchFolders holds the folders of a gateway's scratch sessions, which run without a project folder. Each gateway
// process has a folder of its own in one folder per user, and locks it while it runs, so that a gateway removes only
// the folders of gateways that ended without removing theirs.
type scratchFolders struct {
	dir  string
	lock *os.File
}

func scratchRootName() string { return fmt.Sprintf("gripi-scratch-%d", os.Getuid()) }

func openScratchFolders(base string) (*scratchFolders, error) {
	root := filepath.Join(base, scratchRootName())
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("%s is not a private folder of this user", root)
	}
	removeAbandonedScratchFolders(root)
	dir, err := os.MkdirTemp(root, "gateway-")
	if err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			lock.Close()
		}
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &scratchFolders{dir: dir, lock: lock}, nil
}

func removeAbandonedScratchFolders(root string) {
	entries, _ := os.ReadDir(root)
	for _, item := range entries {
		dir := filepath.Join(root, item.Name())
		lock, err := os.Open(filepath.Join(dir, "lock"))
		if err != nil {
			// A gateway that is starting may not have made its lock yet.
			if info, err := os.Lstat(dir); err == nil && time.Since(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(dir)
			}
			continue
		}
		if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil {
			_ = os.RemoveAll(dir)
		}
		lock.Close()
	}
}

// create makes the folders of a new scratch session: work, where Pi runs, and tmp, its TMPDIR.
func (folders *scratchFolders) create() (string, error) {
	dir, err := os.MkdirTemp(folders.dir, "session-")
	if err != nil {
		return "", err
	}
	for _, name := range []string{"work", "tmp"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

func (folders *scratchFolders) Close() error {
	err := os.RemoveAll(folders.dir)
	folders.lock.Close()
	return err
}
