package mirror

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"math"
)

// Changed files at least this large are sent as rsync-style deltas against
// what the destination has: packs and the index otherwise travel whole.
const deltaMinSize = 256 << 10

// chunkSize bounds the data in one frame.
const chunkSize = 1 << 20

type blockSum struct {
	Weak   uint32
	Strong [16]byte
}

// deltaOp copies Len bytes at Off from a put's base, or carries Data.
type deltaOp struct {
	Base int
	Off  int64
	Len  int64
	Data []byte
}

func blockSize(size int64) int {
	return min(max(int(math.Sqrt(float64(size)))&^7, 700), 128<<10)
}

// weakSum is rsync's rolling checksum.
func weakSum(b []byte) (a, s uint32) {
	n := uint32(len(b))
	for i, c := range b {
		a += uint32(c)
		s += (n - uint32(i)) * uint32(c)
	}
	return a, s
}

func strongSum(b []byte) [16]byte {
	sum := sha256.Sum256(b)
	return [16]byte(sum[:16])
}

// blockSums lists the checksums of r's whole blocks; a shorter tail is left out.
func blockSums(r io.Reader, size int) ([]blockSum, error) {
	var sums []blockSum
	br := bufio.NewReaderSize(r, 1<<20)
	block := make([]byte, size)
	for {
		if _, err := io.ReadFull(br, block); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return sums, nil
			}
			return nil, err
		}
		a, s := weakSum(block)
		sums = append(sums, blockSum{a&0xffff | s<<16, strongSum(block)})
	}
}

// diff describes r as copies of the destination's blocks and literal data.
func diff(r io.Reader, size int, sums [][]blockSum, emit func(deltaOp) error) error {
	type ref struct{ base, block int }
	index := map[uint32][]ref{}
	for base, list := range sums {
		for i, sum := range list {
			index[sum.Weak] = append(index[sum.Weak], ref{base, i})
		}
	}
	find := func(window []byte, weak uint32) (ref, bool) {
		refs := index[weak]
		if refs == nil {
			return ref{}, false
		}
		strong := strongSum(window)
		for _, ref := range refs {
			if sums[ref.base][ref.block].Strong == strong {
				return ref, true
			}
		}
		return ref{}, false
	}

	buf := make([]byte, 0, 8<<20)
	lit, pos := 0, 0 // buf[lit:pos] waits to be sent as data; the window starts at pos
	eof := false
	var a, s uint32
	rolling := false
	literal := func() error {
		if pos > lit {
			if err := emit(deltaOp{Data: buf[lit:pos]}); err != nil {
				return err
			}
		}
		lit = pos
		return nil
	}
	for {
		if len(buf)-pos <= size && !eof { // rolling needs the byte after the window
			if err := literal(); err != nil {
				return err
			}
			n := copy(buf, buf[pos:])
			buf, lit, pos = buf[:n], 0, 0
			for len(buf) < cap(buf) && !eof {
				m, err := r.Read(buf[len(buf):cap(buf)])
				buf = buf[:len(buf)+m]
				if errors.Is(err, io.EOF) {
					eof = true
				} else if err != nil {
					return err
				}
			}
		}
		if len(buf)-pos < size {
			break
		}
		window := buf[pos : pos+size]
		if !rolling {
			a, s = weakSum(window)
			rolling = true
		}
		if ref, ok := find(window, a&0xffff|s<<16); ok {
			if err := literal(); err != nil {
				return err
			}
			if err := emit(deltaOp{Base: ref.base, Off: int64(ref.block) * int64(size), Len: int64(size)}); err != nil {
				return err
			}
			pos += size
			lit = pos
			rolling = false
			continue
		}
		if pos+size < len(buf) {
			out, in := uint32(buf[pos]), uint32(buf[pos+size])
			a = a - out + in
			s = s - uint32(size)*out + a
		} else {
			rolling = false
		}
		pos++
	}
	pos = len(buf)
	return literal()
}

// putWriter splits a put into frames, merging adjacent copies.
type putWriter struct {
	c    *conn
	f    *frame
	data int
}

func (w *putWriter) add(op deltaOp) error {
	if len(op.Data) == 0 {
		if n := len(w.f.Ops); n > 0 {
			last := &w.f.Ops[n-1]
			if len(last.Data) == 0 && last.Base == op.Base && last.Off+last.Len == op.Off {
				last.Len += op.Len
				return nil
			}
		}
		w.f.Ops = append(w.f.Ops, op)
		return w.sendFull()
	}
	for data := op.Data; len(data) > 0; {
		n := min(len(data), chunkSize-w.data)
		w.f.Ops = append(w.f.Ops, deltaOp{Data: bytes.Clone(data[:n])})
		w.data += n
		data = data[n:]
		if err := w.sendFull(); err != nil {
			return err
		}
	}
	return nil
}

func (w *putWriter) sendFull() error {
	if w.data < chunkSize && len(w.f.Ops) < 4096 {
		return nil
	}
	if err := w.c.send(w.f); err != nil {
		return err
	}
	w.f, w.data = &frame{Kind: kindPut, Path: w.f.Path}, 0
	return nil
}

func (w *putWriter) finish(abort bool, hash []byte) error {
	w.f.Last, w.f.Abort, w.f.Hash = true, abort, hash
	return w.c.send(w.f)
}
