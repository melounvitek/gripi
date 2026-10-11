package hermes

import (
	"context"
	"os"
)

// Check starts Hermes's ACP server at path and reports why Gripi could not run Hermes sessions with it.
func Check(ctx context.Context, path string) error {
	conn, err := start(path, environment(os.Environ()), nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.initialize(ctx)
}
