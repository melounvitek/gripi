//go:build !linux

package reconcile

import "golang.org/x/sys/unix"

func syncFilesystem(string) error { return unix.Sync() }
