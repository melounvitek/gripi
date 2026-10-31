package mirror

import (
	"os"

	"golang.org/x/sys/unix"
)

// syncFilesystem makes everything written to dir's filesystem durable.
func syncFilesystem(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}
