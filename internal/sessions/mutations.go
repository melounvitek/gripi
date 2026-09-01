package sessions

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func DeleteSessionFile(path string) (string, error) {
	trashArgs := []string{path}
	if len(path) > 0 && path[0] == '-' {
		trashArgs = []string{"--", path}
	}
	commands := [][]string{append([]string{"trash"}, trashArgs...), {"gio", "trash", "--", path}}
	for _, command := range commands {
		if err := exec.Command(command[0], command[1:]...).Run(); err == nil {
			return "trash", nil
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return "trash", nil
		}
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("delete session: %w", err)
	}
	return "unlink", nil
}

func (cache *Cache) Forget(path string) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if item := cache.items[path]; item != nil {
		cache.bytes -= item.index.bytes
		delete(cache.items, path)
	}
	if item := cache.metadataItems[path]; item != nil {
		cache.metadataBytes -= item.bytes
		cache.metadataOrder.Remove(item.element)
		delete(cache.metadataItems, path)
	}
}
