//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package main

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

func lockPasswordFile(path string) (func(), error) {
	lock := path + ".lock"
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := os.Mkdir(lock, 0700); err == nil {
			return func() { _ = os.Remove(lock) }, nil
		} else if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting to create admin password")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
