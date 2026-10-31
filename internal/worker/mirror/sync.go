package mirror

import (
	"context"
	"io"
	"path/filepath"
)

// Sync makes the destination at the other end of stream match unit once: the
// destination lists what it has, and only the differences travel. It returns
// after the destination has applied them durably, and closes stream if it can.
// It is meant for a unit nobody changes meanwhile, such as the first copy to a
// worker; a Source does the same when it starts.
func Sync(ctx context.Context, stream io.ReadWriter, unit Unit, opts Options) error {
	s := newSender(unit, opts, stream)
	defer s.close()
	defer closeOnDone(ctx, stream)()
	seen, err := s.scan()
	if err != nil {
		return err
	}
	if err := s.handshake(); err != nil {
		return err
	}
	s.markMissing(seen)
	stopped := s.startReceiving()
	// Objects go after the metadata that names them, as at a barrier.
	if err := s.flush(true, s.isMeta); err != nil {
		return err
	}
	if s.gitDir != "" {
		if err := s.walk(filepath.Join(s.gitDir, "objects"), nil); err != nil {
			return err
		}
	}
	if err := s.flush(true, nil); err != nil {
		return err
	}
	for p := range s.objDels {
		s.objDels[p] = 0 // nobody changes the unit, so no ref read later can name them
	}
	ack, err := s.sendBarrier(s.next)
	if err != nil {
		return err
	}
	select {
	case err = <-ack:
	case err = <-stopped:
	case <-ctx.Done():
		err = ctx.Err()
	}
	return err
}

// closeOnDone closes stream, if it can be closed, when ctx is done or the
// returned function is called.
func closeOnDone(ctx context.Context, stream io.ReadWriter) func() {
	closer, ok := stream.(io.Closer)
	if !ok {
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() { closer.Close() })
	return func() {
		stop()
		closer.Close()
	}
}
