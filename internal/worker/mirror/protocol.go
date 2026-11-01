// Package mirror keeps a copy of a repository unit (a main folder, its linked
// worktrees and their common .git) up to date over one stream, applying changes
// only at barriers where the copy's .git is consistent.
package mirror

import (
	"bufio"
	"encoding/gob"
	"io"
)

// version changes whenever frames change; both ends must run the same Gripi.
const version = 1

type kind uint8

const (
	kindHello      kind = iota + 1 // source → destination: the unit's folders, and ignored ones not to list
	kindManifest                   // destination → source: what it has, and its last barrier
	kindRoots                      // source → destination: the unit's folders changed
	kindPut                        // source → destination: file contents in one or more frames
	kindMkdir                      // source → destination
	kindSymlink                    // source → destination
	kindDelete                     // source → destination
	kindMove                       // source → destination
	kindSigRequest                 // source → destination: block signatures of files it has
	kindSigs                       // destination → source
	kindBarrier                    // source → destination: apply everything received so far
	kindAck                        // destination → source: the barrier is applied durably, or Error
	kindFail                       // destination → source: the destination stopped because of Error
)

type frame struct {
	Kind    kind
	Version int
	Path    string
	To      string
	Target  string
	Mode    uint32
	Mtime   int64
	Size    int64
	// A put starts with First and ends with Last; Abort drops it because the
	// file changed while it was read. Ops copy from Bases (files the destination
	// already has) or carry data; Hash is the SHA-256 of a file built from Bases.
	First, Last, Abort bool
	Bases              []string
	Ops                []deltaOp
	Hash               []byte
	N                  uint64
	Roots              []string
	Skip               []string
	Entries            []entry
	BlockSize          int
	Sums               [][]blockSum
	Error              string
}

type entry struct {
	Path   string
	Type   byte // 'f', 'd' or 'l'
	Mode   uint32
	Size   int64
	Mtime  int64
	Target string
	ino    uint64 // the source's inode of what it sent; not on the wire
	// With CompleteLines, the length and SHA-256 of what the source sent, the file's complete lines; nil when it
	// doesn't know. Not on the wire.
	lines    int64
	linesSum []byte
}

type conn struct {
	w   *bufio.Writer
	enc *gob.Encoder
	dec *gob.Decoder
}

func newConn(rw io.ReadWriter) *conn {
	w := bufio.NewWriterSize(rw, 1<<20)
	return &conn{w: w, enc: gob.NewEncoder(w), dec: gob.NewDecoder(bufio.NewReaderSize(rw, 1<<20))}
}

func (c *conn) send(f *frame) error { return c.enc.Encode(f) }

func (c *conn) flush() error { return c.w.Flush() }

func (c *conn) receive() (*frame, error) {
	var f frame
	err := c.dec.Decode(&f)
	return &f, err
}
