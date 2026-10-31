//go:build !linux

package mirror

import "golang.org/x/sys/unix"

func syncFilesystem(string) error { return unix.Sync() }
