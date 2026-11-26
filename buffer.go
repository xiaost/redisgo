package redisgo

import (
	"bytes"
	"io"
	"time"
)

const (
	minReadBufferSize = 100
	// maxEmptyReads bounds consecutive (0, nil) reads before giving up,
	// like bufio does, so a misbehaving io.Reader cannot spin the reader.
	maxEmptyReads = 100
	// maxReuseBuf bounds the size of a buffer reused by reclaim, so a
	// buffer grown for one large reply is not kept by the conn. It fits a
	// 64KB value with its CRLF and the room readmore leaves for a header.
	maxReuseBuf = 64<<10 + 2 + minReadBufferSize
)

type readDeadliner interface {
	SetReadDeadline(t time.Time) error
}

// reader is an append-only buffered reader. Slices handed out by Read and
// Readline alias the buffer, so memory that may still be referenced is never
// overwritten: when the buffer runs out of room a new one is allocated and
// the old one is left to whatever still references it.
//
// Bytes that have been consumed but never exposed to the user are the
// exception, see reclaim and expose.
type reader struct {
	rd  io.Reader
	b   []byte
	r   int
	w   int
	sz  int
	pin int // b[:pin] may be referenced by exposed replies, pin <= r

	// A read deadline of now+timeout is set before the first read from rd
	// after disarm, so replies already buffered cost no deadline update.
	dl      readDeadliner
	timeout time.Duration
	armed   bool
}

func newReader(r io.Reader, sz int) *reader {
	if sz < minReadBufferSize {
		sz = minReadBufferSize
	}
	br := new(reader)
	br.rd = r
	br.dl, _ = r.(readDeadliner)
	br.b = make([]byte, sz)
	br.sz = sz
	return br
}

// disarm makes the next read from rd set a new read deadline.
func (r *reader) disarm() {
	r.armed = false
}

// expose marks every byte consumed so far as possibly referenced by the
// user, so it is never overwritten.
func (r *reader) expose() {
	r.pin = r.r
}

// drop lets go of a drained buffer larger than reclaim reuses, so the conn
// does not keep the memory of one large reply until its next read. Slices
// handed out keep the buffer alive as long as they need it.
func (r *reader) drop() {
	if r.r == r.w && len(r.b) > max(r.sz, maxReuseBuf) {
		r.b, r.r, r.w, r.pin = nil, 0, 0, 0
	}
}

// reclaim makes the space of the bytes consumed since the last expose
// reusable. It must only be called between replies, after everything that
// referenced those bytes has been copied or dropped. It only takes effect
// when no unread data is buffered, so nothing has to be moved.
func (r *reader) reclaim() {
	if r.r == r.w && len(r.b) <= max(r.sz, maxReuseBuf) {
		r.r, r.w = r.pin, r.pin
	}
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
	r.pin = 0
}

// read reads from rd into the free space of the buffer, setting the read
// deadline first if needed.
func (r *reader) read() (int, error) {
	if !r.armed {
		r.armed = true
		if r.timeout > 0 && r.dl != nil {
			r.dl.SetReadDeadline(time.Now().Add(r.timeout))
		}
	}
	return r.rd.Read(r.buf())
}

// readmore reads at least n more bytes into the buffer.
func (r *reader) readmore(n int) error {
	if r.free() < n {
		// leave room for the header of a reply of the same size, so a
		// reused buffer fits it
		r.grow(r.buffered() + n + minReadBufferSize)
	}
	empty := 0
	for n > 0 {
		readn, err := r.read()
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
		readn, err := r.read()
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
