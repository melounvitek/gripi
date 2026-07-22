//go:build !windows

package server

import "golang.org/x/sys/unix"

func directoryAccessible(path string) bool {
	return unix.Access(path, unix.R_OK|unix.X_OK) == nil
}
