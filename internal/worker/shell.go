// Package worker connects main to its workers: SSH connections, the helper on each worker and its control protocol.
package worker

import "strings"

// ShellQuote quotes words for the worker user's shell, which ssh runs every remote command in. Single quotes mean
// the same in sh, bash, zsh and fish, except for backslashes, which fish treats specially inside them.
func ShellQuote(words ...string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}
