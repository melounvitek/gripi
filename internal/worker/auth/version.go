package auth

import (
	"regexp"
	"strconv"
)

// MinimumPiVersion is the oldest Pi that workers support. Older ones lack `pi auth` or hooks that Gripi's worker
// extension relies on.
const MinimumPiVersion = "0.99.0"

var piVersionPattern = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// OlderPi reports whether version, as `pi --version` prints it, is older than MinimumPiVersion. A version it can't
// read counts as older.
func OlderPi(version string) bool {
	have, need := piVersionPattern.FindStringSubmatch(version), piVersionPattern.FindStringSubmatch(MinimumPiVersion)
	if have == nil {
		return true
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(have[i])
		b, _ := strconv.Atoi(need[i])
		if a != b {
			return a < b
		}
	}
	return false
}
