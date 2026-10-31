package gripi

import "embed"

// WebFiles contains the frontend files needed by the standalone gateway binary.
//
//go:embed public pi_extensions/gripi-tree.ts pi_extensions/gripi-worker.ts
var WebFiles embed.FS
