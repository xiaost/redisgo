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
	rd   io.Reader
	b    []byte
	r    int
	w    int
	sz   int
	pin  int // b[:pin] may be referenced by exposed replies, pin <= r
	safe int // >= 0: no in-flight slice references b[pin:safe]; < 0: standalone/exposed

	// A read deadline of now+timeout, or limit if earlier, is set before
	// the first read from rd after disarm, so replies already buffered cost
	// no deadline update. The Conn arms its write deadline the same way.
	dl       readDeadliner
	timeout  time.Duration
	limit    time.Time // see Conn.setLimit
	hasLimit bool
	armed    bool
	dlset    bool // a read deadline is set on dl
	byLimit  bool // the last deadline set, read or write, is limit
}

func (r *reader) init(rd io.Reader, sz int) {
	if sz < minReadBufferSize {
		sz = minReadBufferSize
	}
	r.rd = rd
	r.dl, _ = rd.(readDeadliner)
	r.b = make([]byte, sz)
	r.sz = sz
	r.safe = -1
}

func newReader(r io.Reader, sz int) *reader {
	var br reader
	br.init(r, sz)
	return &br
}

// disarm makes the next read from rd set a new read deadline.
func (r *reader) disarm() {
	r.armed = false
}

// expose marks every byte consumed so far as possibly referenced by the
// user, so it is never overwritten.
func (r *reader) expose() {
	r.pin = r.r
	r.safe = -1
}

// drop lets go of a drained buffer larger than reclaim reuses, so the conn
// does not keep the memory of one large reply until its next read. Slices
// handed out keep the buffer alive as long as they need it.
func (r *reader) drop() {
	if r.r == r.w && len(r.b) > max(r.sz, maxReuseBuf) {
		r.b, r.r, r.w, r.pin = nil, 0, 0, 0
		if r.safe >= 0 {
			r.safe = 0
		}
	}
}

// reclaim makes the space of the bytes consumed since the last expose
// reusable. It must only be called between replies, after everything that
// referenced those bytes has been copied or dropped. When unread pipelined
// bytes remain and the tail is nearly full, it shifts them down to pin once
// enough dead space has accumulated to amortize the copy.
func (r *reader) reclaim() {
	if r.r == r.w {
		if len(r.b) <= max(r.sz, maxReuseBuf) {
			r.r, r.w = r.pin, r.pin
		}
	} else if r.r > r.pin {
		r.reclaimShift()
	}
	r.safe = r.r
}

func (r *reader) reclaimShift() {
	if len(r.b) <= max(r.sz, maxReuseBuf) && len(r.b)-r.w < minReadBufferSize && r.r-r.pin >= r.w-r.r {
		n := copy(r.b[r.pin:], r.b[r.r:r.w])
		r.r = r.pin
		r.w = r.pin + n
	}
}

// compact shifts unread bytes down to pin when nothing in the current reply
// references b[pin:r] and doing so provides at least need free bytes in a
// reusable buffer.
func (r *reader) compact(need int) bool {
	if r.safe < 0 || r.r != r.safe || r.r <= r.pin {
		return false
	}
	if len(r.b) > max(r.sz, maxReuseBuf) {
		return false
	}
	if len(r.b)-r.pin-r.buffered() < need {
		return false
	}
	n := copy(r.b[r.pin:], r.b[r.r:r.w])
	r.r = r.pin
	r.w = r.pin + n
	r.safe = r.pin
	return true
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
	if limit := max(r.sz, maxReuseBuf); r.safe >= 0 && r.r > r.safe && len(r.b) <= limit && (r.pin == 0 || r.r-r.safe >= r.sz) {
		// Mid-reply growth on a buffer when unpinned or when the current reply
		// already exceeded r.sz (e.g. a multi-bulk array larger than r.b): grow
		// geometrically up to the reuse limit so subsequent replies of the same
		// size fit in one buffer and read.
		cap2 := limit
		if len(r.b) < limit/2 {
			cap2 = 2 * len(r.b)
		}
		if sz < cap2 {
			sz = cap2
		}
	}
	if sz < r.sz {
		sz = r.sz
	}
	b := r.bytes()
	r.b = make([]byte, sz)
	r.r = 0
	r.w = copy(r.b, b)
	r.pin = 0
	if r.safe >= 0 {
		r.safe = 0
	}
}

// read reads from rd into the free space of the buffer, setting the read
// deadline first if needed.
func (r *reader) read() (int, error) {
	if !r.armed && (r.timeout > 0 || r.hasLimit || r.dlset) && r.dl != nil {
		if err := r.arm(); err != nil {
			return 0, err
		}
	}
	return r.rd.Read(r.buf())
}

// arm sets the read deadline of the current reply, or lifts the one of an
// earlier reply if no deadline applies.
func (r *reader) arm() error {
	r.armed = true
	t := r.deadline(r.timeout)
	if err := r.dl.SetReadDeadline(t); err != nil {
		return err
	}
	r.dlset = !t.IsZero()
	return nil
}

// deadline returns the deadline of an I/O starting now: now+timeout if
// timeout is positive, or limit if set and earlier. The zero time means
// no deadline. It records whether limit is the deadline in byLimit.
func (r *reader) deadline(timeout time.Duration) time.Time {
	var t time.Time
	if timeout > 0 {
		t = time.Now().Add(timeout)
	}
	r.byLimit = r.hasLimit && (t.IsZero() || r.limit.Before(t))
	if r.byLimit {
		t = r.limit
	}
	return t
}

// readmore reads at least n more bytes into the buffer.
func (r *reader) readmore(n int) error {
	if r.free() < n && !r.compact(n) {
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
	if r.free() < minReadBufferSize && !r.compact(minReadBufferSize) {
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
