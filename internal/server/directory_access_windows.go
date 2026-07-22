//go:build windows

package server

import "os"

func directoryAccessible(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	return file.Close() == nil
}
