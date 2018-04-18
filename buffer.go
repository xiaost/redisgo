package redisgo

import (
	"bytes"
	"io"
)

const minReadBufferSize = 100

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

func (r *reader) readmore(n int) error {
	if r.free() < n {
		sz := r.sz
		b := r.bytes()
		for sz-len(b) < n {
			sz += r.sz
		}
		r.b = make([]byte, sz)
		r.r = 0
		r.w = copy(r.b, b)
	}
	readn, err := io.ReadAtLeast(r.rd, r.buf(), n)
	if readn > 0 {
		r.w += readn
	}
	return err
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

func (r *reader) fillmore() error {
	if r.free() < minReadBufferSize || r.buffered() > r.sz/2 {
		b := r.bytes()
		sz := 2 * len(b)
		if sz < r.sz {
			sz = r.sz
		}
		r.b = make([]byte, sz)
		r.r = 0
		r.w = copy(r.b, b)
	}
	readn, err := r.rd.Read(r.buf())
	if readn > 0 {
		r.w += readn
		if err == io.EOF {
			err = nil
		}
	}
	return err
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
