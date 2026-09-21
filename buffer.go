package redisgo

import (
	"bytes"
	"io"
)

const (
	minReadBufferSize = 100
	// maxEmptyReads bounds consecutive (0, nil) reads before giving up,
	// like bufio does, so a misbehaving io.Reader cannot spin the reader.
	maxEmptyReads = 100
)

// reader is an append-only buffered reader. Slices handed out by Read and
// Readline alias the buffer, so buffered memory is never overwritten: when
// the buffer runs out of room a new one is allocated and the old one is
// left to whatever still references it.
type reader struct {
	rd io.Reader
	b  []byte
	r  int
	w  int
	sz int
}

func newReader(r io.Reader, sz int) *reader {
	if sz < minReadBufferSize {
		sz = minReadBufferSize
	}
	br := new(reader)
	br.rd = r
	br.b = make([]byte, sz)
	br.sz = sz
	return br
}

func (r *reader) buffered() int {
	return r.w - r.r
}

func (r *reader) free() int {
	return len(r.b) - r.w
}

func (r *reader) bytes() []byte {
	return r.b[r.r:r.w]
}

func (r *reader) buf() []byte {
	return r.b[r.w:]
}

// grow replaces the buffer with a new one of at least sz bytes holding the
// buffered data. The old buffer stays intact for slices handed out earlier.
func (r *reader) grow(sz int) {
	if sz < r.sz {
		sz = r.sz
	}
	b := r.bytes()
	r.b = make([]byte, sz)
	r.r = 0
	r.w = copy(r.b, b)
}

// readmore reads at least n more bytes into the buffer.
func (r *reader) readmore(n int) error {
	if r.free() < n {
		r.grow(r.buffered() + n)
	}
	empty := 0
	for n > 0 {
		readn, err := r.rd.Read(r.buf())
		r.w += readn
		n -= readn
		if err != nil {
			if err == io.EOF && n <= 0 {
				return nil
			}
			return err
		}
		if readn > 0 {
			empty = 0
			continue
		}
		if empty++; empty >= maxEmptyReads {
			return io.ErrNoProgress
		}
	}
	return nil
}

// Read reads n bytes from underlying reader
func (r *reader) Read(n int) (b []byte, err error) {
	if bn := r.buffered(); bn < n {
		if err = r.readmore(n - bn); err != nil {
			return
		}
	}
	b = r.bytes()[:n:n]
	r.r += n
	return
}

// fillmore reads whatever is available into the buffer, growing it
// geometrically when it is nearly full.
func (r *reader) fillmore() error {
	if r.free() < minReadBufferSize {
		r.grow(2 * r.buffered())
	}
	for empty := 0; ; empty++ {
		readn, err := r.rd.Read(r.buf())
		if readn > 0 {
			r.w += readn
			if err == io.EOF {
				err = nil
			}
			return err
		}
		if err != nil {
			return err
		}
		if empty >= maxEmptyReads {
			return io.ErrNoProgress
		}
	}
}

// Readline reads line ending with CRLF
func (r *reader) Readline() (b []byte, err error) {
	if r.buffered() == 0 {
		err = r.fillmore()
	}
	start := 0
	for err == nil {
		p := r.bytes()
		pos := bytes.IndexByte(p[start:], LF)
		if pos > 0 && p[start+pos-1] == CR {
			b = p[: start+pos+1 : start+pos+1]
			r.r += (start + pos + 1)
			return
		}
		if pos >= 0 {
			start += pos + 1
			continue
		}
		err = r.fillmore()
	}
	return
}
