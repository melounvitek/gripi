package sessions

import (
	"os"
	"syscall"
)

func nativeFileIdentity(stat os.FileInfo) (uint64, uint64, bool) {
	system, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(system.Dev), uint64(system.Ino), true
}
