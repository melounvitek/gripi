//go:build !linux

package mirror

import (
	"context"
	"errors"
	"io"
	"io/fs"
)

var errUnsupported = errors.New("mirroring a unit needs Linux")

// Source watches the live copy of a unit; it needs inotify, so only Linux has it.
type Source struct{}

func NewSource(Unit, Options) *Source { return &Source{} }

func (*Source) Healthy() bool { return false }

func (*Source) Barrier(context.Context) (uint64, []string, error) { return 0, nil, errUnsupported }

func (*Source) Run(context.Context, io.ReadWriter) error { return errUnsupported }

func openForWriting(fs.FileInfo) bool { return true }
