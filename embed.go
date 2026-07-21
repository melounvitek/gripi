package gripi

import "embed"

// WebFiles contains the frontend files needed by the standalone gateway binary.
//
//go:embed public
var WebFiles embed.FS
