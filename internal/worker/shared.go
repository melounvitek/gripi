package worker

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CheckSeparate refuses the worker at destination when it sees one of main's folders as its own, at the same path,
// such as when it is this machine as the same user, or mounts the same home folder. Gripi would then replace main's
// own files there with the worker's, such as Pi's logins with logins without refresh tokens, or main's gripi socket.
// It leaves a file with a random name in each of the folders that exist here, and run runs a sh script with their
// paths on the worker and returns what the script printed.
func CheckSeparate(destination string, run func(script string, arguments ...string) (string, error), folders ...string) error {
	var markers []string
	defer func() {
		for _, marker := range markers {
			os.Remove(marker)
		}
	}()
	for _, folder := range folders {
		if folder == "" {
			continue
		}
		file, err := os.CreateTemp(folder, ".gripi-worker-check-*")
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		file.Close()
		markers = append(markers, file.Name())
	}
	if len(markers) == 0 {
		return nil
	}
	output, err := run(`for marker; do if [ -e "$marker" ]; then echo "$marker"; fi; done`, markers...)
	if err != nil {
		return err
	}
	seen := strings.Split(strings.TrimSpace(output), "\n")
	for _, marker := range markers {
		if slices.Contains(seen, marker) {
			return refuse("%s shares this machine's %s, so Gripi would replace this machine's own files with the worker's, such as Pi's logins; a worker needs a user with files of its own on another computer",
				destination, filepath.Dir(marker))
		}
	}
	return nil
}
