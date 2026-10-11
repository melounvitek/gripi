package rpc

import (
	"slices"
	"strings"
)

var internalCommandNames = []string{"gripi_reload", "gripi_tree_navigate", "gripi_tree_snapshot", "gripi_tree_leaf", "gripi_tree_label", "gripi_scoped_models"}

// InternalCommand reports whether Pi would run the message as one of the commands
// that carry the gateway's own requests to its Pi extension.
func InternalCommand(message string) bool {
	name, _, _ := strings.Cut(message, " ")
	return strings.HasPrefix(name, "/") && slices.Contains(internalCommandNames, name[1:])
}
