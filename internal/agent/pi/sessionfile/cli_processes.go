package sessionfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CLIProcesses finds the processes, other than the gateway's own, whose arguments name the session as Pi CLI takes
// one: --session with its path or the start of its ID, or --session-id with its ID. Pi CLI keeps no session file open
// between its writes, so its arguments are all that tell. It misses Pi CLI started with --continue or --resume, and
// processes that /proc doesn't show, such as other users'.
func (synchronizer *Synchronizer) CLIProcesses(path string) ([]int, error) {
	id, err := sessionID(path)
	if err != nil {
		return nil, err
	}
	processes, err := os.ReadDir("/proc")
	// Without /proc, as on macOS, nothing can tell.
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []int
	for _, process := range processes {
		pid, err := strconv.Atoi(process.Name())
		if err != nil {
			continue
		}
		// A process that ended meanwhile or that /proc doesn't show has no arguments to read.
		commandLine, err := os.ReadFile(filepath.Join("/proc", process.Name(), "cmdline"))
		if err != nil || !namesSession(strings.Split(string(commandLine), "\x00"), pid, path, id) || descendsFrom(pid, os.Getpid()) {
			continue
		}
		found = append(found, pid)
	}
	return found, nil
}

func namesSession(arguments []string, pid int, path, id string) bool {
	for index, argument := range arguments {
		name, value, joined := strings.Cut(argument, "=")
		if !joined {
			if index+1 >= len(arguments) {
				continue
			}
			value = arguments[index+1]
		}
		switch name {
		case "--session-id":
			if value == id {
				return true
			}
		case "--session":
			// A relative path is relative to the process's directory.
			if directory, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd")); err == nil && !filepath.IsAbs(value) {
				if filepath.Join(directory, value) == path {
					return true
				}
			}
			if value == path || value != "" && strings.HasPrefix(id, value) {
				return true
			}
		}
	}
	return false
}

// descendsFrom reports whether a process was started by ancestor, or by a process that it started.
func descendsFrom(pid, ancestor int) bool {
	for pid > 1 {
		stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return false
		}
		// The process's name, in parentheses, may hold spaces and parentheses itself.
		end := bytes.LastIndexByte(stat, ')')
		fields := strings.Fields(string(stat[end+1:]))
		if end < 0 || len(fields) < 2 {
			return false
		}
		if pid, err = strconv.Atoi(fields[1]); err != nil {
			return false
		}
		if pid == ancestor {
			return true
		}
	}
	return false
}

// sessionID reads the ID from the header that starts a session file.
func sessionID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	line, err := bufio.NewReaderSize(file, 64<<10).ReadSlice('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	var header struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if json.Unmarshal(line, &header) != nil || header.Type != "session" || header.ID == "" {
		return "", errors.New("the session file has no header with its ID")
	}
	return header.ID, nil
}
