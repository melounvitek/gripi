package auth

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	// staleLock is how long Pi waits for its lock before it counts as left behind. A Pi holding it renews it every
	// 15 seconds (proper-lockfile).
	staleLock = 30 * time.Second
	lockRetry = 20 * time.Millisecond
)

// WriteLocked replaces the file at path with contents, under the lock Pi takes on it with proper-lockfile: the
// folder path+".lock". A Pi that saves a login meanwhile therefore can't overwrite contents with what it read
// before. The new file is written beside it and renamed into place, so readers without the lock never see it
// half-written.
func WriteLocked(ctx context.Context, path string, contents []byte) error {
	lock := path + ".lock"
	for {
		err := os.Mkdir(lock, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		// Like proper-lockfile, take over a stale lock by removing it and trying again.
		if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > staleLock {
			if err := os.Remove(lock); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockRetry):
		}
	}
	return errors.Join(replace(path, contents), os.Remove(lock))
}

func replace(path string, contents []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	_, err = file.Write(contents)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(file.Name(), path)
	}
	if err != nil {
		os.Remove(file.Name())
	}
	return err
}
