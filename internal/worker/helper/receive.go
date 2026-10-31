package helper

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// Receive makes the folder to a copy of the other side's folder from, which the other side sends with mirror.Sync over
// input and output. When from and to are the same, the copy keeps the other side's paths, also for the other
// folders of from's unit, such as linked worktrees. While it runs, it keeps the copy's state next to to, on the same
// file system.
func Receive(ctx context.Context, input io.Reader, output io.Writer, from, to string) error {
	state := filepath.Join(filepath.Dir(to), "."+filepath.Base(to)+".gripi-receive")
	// What an interrupted copy left there is sent again, because the sender compares what the folder has.
	if err := os.RemoveAll(state); err != nil {
		return err
	}
	defer os.RemoveAll(state)
	if from == to {
		from, to = "/", "/"
	}
	destination, err := mirror.NewMovedDestination(state, from, to)
	if err != nil {
		return err
	}
	stream := struct {
		io.Reader
		io.Writer
	}{input, output}
	return destination.Serve(ctx, stream, filepath.Join(state, "aside"))
}
