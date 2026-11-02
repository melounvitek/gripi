package reconcile

import (
	"os"

	"golang.org/x/sys/unix"
)

func syncFilesystem(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return unix.Syncfs(int(f.Fd()))
}
