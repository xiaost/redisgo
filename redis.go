package redisgo

import (
	"bufio"
	"errors"
	"iter"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Conn represents a redis client.
//
// A Conn is not safe for concurrent use. Use a Pool to hand one Conn to
// each goroutine.
//
// Byte slices returned through Reply, DoBytes, DoBytesSlice and RedisErr
// alias the read buffer of the Conn. They stay valid after Reply.Free, but
// keeping one alive keeps the whole buffer alive, so copy values that are
// retained for long. DoString, DoInteger and DoNoReply return nothing that
// aliases the buffer (except a RedisErr), which lets the Conn reuse it.
type Conn struct {
	lease atomic.Uint64 // first field for 8-byte alignment on 32-bit; counts PoolConn borrows put back

	conn  net.Conn
	br    reader
	bw    bufio.Writer
	cmd   []byte   // scratch for commands larger than bw (up to maxCmdBuf)
	reply Reply    // scratch for the DoXxx helpers, see do
	pipe  Pipeline // scratch for Pool.Pipeline

	err    error
	closed bool
	pd     int   // replies pending from Send
	state  uint8 // session state changed by commands, see track
	ends   uint8 // states the reply endsIn replies from now ends, see endOnSuccess
	endsIn int

	createdAt time.Time // set by the Pool that dialed the conn

	wtimeout time.Duration
	wdlset   bool // a write deadline is set on conn
}

// NewConn creates Conn
func NewConn(conn net.Conn, ops ...Option) *Conn {
	return newConn(conn, applyOptions(ops))
}

func newConn(conn net.Conn, o options) *Conn {
	var r Conn
	r.conn = conn
	r.br.init(conn, o.rbuf)
	r.br.timeout = o.rtimeout
	r.bw = *bufio.NewWriterSize(conn, o.wbuf)
	r.wtimeout = o.wtimeout
	return &r
}

// SetReadTimeout sets the read timeout of underlying connection
func (c *Conn) SetReadTimeout(t time.Duration) {
	if c != nil {
		c.br.timeout = t
	}
}

// SetWriteTimeout sets the write timeout of underlying connection
func (c *Conn) SetWriteTimeout(t time.Duration) {
	if c != nil {
		c.wtimeout = t
	}
}

// SetTimeout sets the read and write timeouts of underlying connection
func (c *Conn) SetTimeout(t time.Duration) {
	c.SetReadTimeout(t)
	c.SetWriteTimeout(t)
}

// Do sends a command to redis and receives its reply.
// Reply.Free() SHOULD be called when no longer used.
//
// Replies still pending from earlier Sends are received and dropped first,
// so Send("MULTI"), Send(...), Do("EXEC") returns the reply of EXEC. If any
// dropped reply is a RedisErr, Do returns the first one instead of the reply.
// The DoXxx helpers behave the same.
func (c *Conn) Do(cmd string, args ...any) (*Reply, error) {
	reply := replyPool.Get().(*Reply)
	if err := c.roundtrip(cmd, args, reply); err != nil {
		reply.Free()
		return nil, err
	}
	if reply.aliasesBuf() {
		c.br.expose() // reply aliases the read buffer from now on
	}
	return reply, nil
}

// roundtrip sends a command and reads its reply into reply. Replies still
// pending from earlier Sends are read into reply and dropped first, and the
// first RedisErr among them is returned after the reply of the command has
// been read, so the conn stays in sync.
func (c *Conn) roundtrip(cmd string, args []any, reply *Reply) error {
	if err := c.send(cmd, args, true); err != nil {
		return err
	}
	var pending error
	for c.pd > 1 {
		if err := c.recv(reply); err != nil {
			return err
		}
		if pending == nil && reply.t == typeError {
			pending = RedisErr(reply.b)
			if len(reply.b) > 0 {
				c.br.expose() // pending aliases the read buffer
			}
		}
	}
	if err := c.recv(reply); err != nil {
		return err
	}
	return pending
}

// Send buffers a command for redis. Nothing is written to the network until
// Flush or Recv is called or the write buffer fills up.
// Send returns ErrInvalidArgType without touching the connection if an arg
// has an unsupported type.
func (c *Conn) Send(cmd string, args ...any) error {
	return c.send(cmd, args, false)
}

// Pending returns the number of commands sent whose replies have not yet
// been received.
func (c *Conn) Pending() int {
	if c == nil {
		return 0
	}
	return c.pd
}

// Pipeline returns a Pipeline bound to c for batching commands and reading
// their replies without allocating a Reply per command.
func (c *Conn) Pipeline() Pipeline {
	return Pipeline{c: c}
}

// send is Send, for Do if do is true. Do relies on one reply per command,
// so it rejects the commands after which redis pushes replies of its own,
// pub/sub and MONITOR, and any command on a conn that sent one.
func (c *Conn) send(cmd string, args []any, do bool) error {
	if err := c.Err(); err != nil {
		return err
	}
	n, size, err := argsInfo(args)
	if err != nil {
		return err
	}
	if do && c.state&statePush != 0 {
		return errPush
	}
	if err := c.track(cmd, n, args, do); err != nil {
		return err
	}
	c.pd++
	need := cmdSize(cmd, size)
	if uint(need) <= uint(c.bw.Available()) {
		// The command fits: encode it straight into the spare capacity of
		// the writer. Nothing is flushed, so no write deadline is needed.
		_, err = c.bw.Write(appendCommand(c.bw.AvailableBuffer(), cmd, n, args))
		return c.seterr(err)
	}
	// bufio flushes while writing a command that does not fit
	if err := c.armWrite(); err != nil {
		return c.seterr(err)
	}
	if uint(need) <= uint(c.bw.Size()) {
		// Command fits in an empty write buffer: flush buffered data first
		// and encode directly into it without allocating c.cmd.
		if err := c.bw.Flush(); err != nil {
			return c.seterr(err)
		}
		_, err = c.bw.Write(appendCommand(c.bw.AvailableBuffer(), cmd, n, args))
		return c.seterr(err)
	}
	if uint(need) <= maxCmdBuf {
		// Pre-size the reusable scratch buffer once so append never reallocates.
		if cap(c.cmd) < need {
			c.cmd = make([]byte, 0, need)
		}
		c.cmd = appendCommand(c.cmd[:0], cmd, n, args)
		_, err = c.bw.Write(c.cmd)
		return c.seterr(err)
	}
	// Stream commands larger than maxCmdBuf through a pooled chunk buffer.
	return c.seterr(writeCommand(&c.bw, cmd, n, args))
}

// Flush writes any buffered data to redis
func (c *Conn) Flush() error {
	if err := c.Err(); err != nil {
		return err
	}
	if c.bw.Buffered() == 0 {
		return nil
	}
	if err := c.armWrite(); err != nil {
		return c.seterr(err)
	}
	return c.seterr(c.bw.Flush())
}

// armWrite sets the write deadline before writing to the network, or lifts
// the one of an earlier write if no deadline applies.
func (c *Conn) armWrite() error {
	if c.wtimeout > 0 || c.br.hasLimit || c.wdlset {
		return c.setWriteDeadline()
	}
	return nil
}

func (c *Conn) setWriteDeadline() error {
	t := c.br.deadline(c.wtimeout)
	if err := c.conn.SetWriteDeadline(t); err != nil {
		return err
	}
	c.wdlset = !t.IsZero()
	return nil
}

// setLimit makes t, if not zero, bound every read and write of c on top of
// the timeouts, until setLimit is called again.
func (c *Conn) setLimit(t time.Time) {
	c.br.limit = t
	c.br.hasLimit = !t.IsZero()
}

// timedOutByLimit reports whether err is a timeout caused by the limit.
func (c *Conn) timedOutByLimit(err error) bool {
	return c.br.byLimit && errors.Is(err, os.ErrDeadlineExceeded)
}

// Recv receives reply from redis
func (c *Conn) Recv(reply *Reply) error {
	if err := c.recv(reply); err != nil {
		return err // the conn has failed, nothing is read any more
	}
	if reply.aliasesBuf() {
		c.br.expose() // reply aliases the read buffer from now on
	}
	return nil
}

// recv is Recv without exposing reply: the buffer space of reply is reused
// by later replies unless c.br.expose is called before the next recv.
func (c *Conn) recv(reply *Reply) error {
	if err := c.Flush(); err != nil {
		return err // reply is left alone
	}
	reply.Reset()
	if c.pd > 0 {
		c.pd-- // a reply without a Send is a pushed message, like pub/sub
	}
	c.br.reclaim()
	c.br.disarm() // the read timeout covers this reply only
	if err := c.read(reply, 0); err != nil {
		return c.seterr(err)
	}
	c.br.drop()
	if c.endsIn > 0 {
		if c.endsIn--; c.endsIn == 0 && reply.t != typeError && !reply.queued() {
			c.state &^= c.ends // EXEC, DISCARD or UNWATCH ran
		}
	}
	return nil
}

// Conn returns the underlying net.Conn. The Conn sets read and write
// deadlines on it as needed, so code using it directly should set its own.
func (c *Conn) Conn() net.Conn {
	if c == nil {
		return nil
	}
	return c.conn
}

// Close flushes buffered commands and closes the underlying connection.
// It returns ErrClosed if the connection was closed already, by Close or
// by an earlier fatal error.
func (c *Conn) Close() error {
	if c == nil || c.closed {
		return ErrClosed
	}
	if err := c.Flush(); err != nil {
		return err // Flush closed the connection already
	}
	c.err = ErrClosed
	c.closed = true
	return c.conn.Close()
}

// Err returns last fatal err.
// if not nil, caller must not reuse the instance
func (c *Conn) Err() error {
	if c == nil {
		return ErrClosed // see PoolConn.Close
	}
	return c.err
}

func (c *Conn) seterr(err error) error {
	if err == nil {
		return nil
	}
	if c.err == nil {
		c.err = err
	} else {
		err = c.err
	}
	if !c.closed {
		c.closed = true
		c.conn.Close()
	}
	return err
}

// maxLen bounds the bulk string length and array size accepted from the
// server. It matches the default proto-max-bulk-len of redis (512MB) and
// protects the client from absurd lengths sent by a broken peer.
const maxLen = 512 << 20

// maxDepth bounds the nesting of arrays. Replies of redis nest a few levels
// at most, while endless nested headers from a broken peer would exhaust the
// stack, which crashes the process instead of failing the conn.
const maxDepth = 64

// maxArrayPrealloc bounds the array capacity allocated up front from a
// single header, so a bogus header cannot trigger a huge allocation.
const maxArrayPrealloc = 1024

// parseInt parses a RESP integer: an optional '+' or '-' followed by
// digits. Up to 18 characters it cannot overflow, which covers every length
// and most integers, so the loop skips the generality of strconv.ParseInt.
func parseInt(b []byte) (int64, error) {
	if len(b) > 18 {
		n, err := strconv.ParseInt(ss(b), 10, 64)
		if err != nil {
			return 0, ErrProtocol
		}
		return n, nil
	}
	d := b
	if len(d) > 0 && (d[0] == '-' || d[0] == '+') {
		d = d[1:]
	}
	if len(d) == 0 {
		return 0, ErrProtocol
	}
	var n int64
	for _, c := range d {
		if c < '0' || c > '9' {
			return 0, ErrProtocol
		}
		n = n*10 + int64(c-'0')
	}
	if b[0] == '-' {
		n = -n
	}
	return n, nil
}

// parseLen parses the length field of a bulk string or array header.
// -1 is valid and denotes a nil value. Unlike integers, lengths take no
// sign otherwise, so -0 or -01 are not valid either.
func parseLen(b []byte) (int, error) {
	if len(b) > 0 && b[0] < '0' { // a sign
		if string(b) == "-1" {
			return -1, nil
		}
		return 0, ErrProtocol
	}
	n, err := parseInt(b)
	if err != nil || n < -1 || n > maxLen {
		return 0, ErrProtocol
	}
	return int(n), nil
}

// read reads a reply into r, which is nested in depth arrays.
func (c *Conn) read(r *Reply, depth int) (err error) {
	var b []byte
	b, err = c.br.Readline()
	if err != nil {
		return
	}
	b = b[:len(b)-2] // remove CRLF
	if len(b) == 0 {
		return ErrProtocol
	}
	t := b[0]
	b = b[1:]
	var n int // for strlen or arraylen
	switch t {
	case '+': // Simple Strings
		if len(b) == 0 {
			r.b = emptyBytes
		} else {
			r.b = b
		}
		r.t = typeSString
	case '$': // Bulk Strings
		if n, err = parseLen(b); err != nil {
			return
		}
		if n < 0 {
			r.t = typeNil
			return
		}
		if depth == 0 && c.br.safe >= 0 {
			c.br.safe = c.br.r // header parsed and not retained
		}
		if r.b, err = c.br.Read(n + 2); err != nil {
			return
		}
		if r.b[n] != CR || r.b[n+1] != LF {
			return ErrProtocol
		}
		if n == 0 {
			r.b = emptyBytes
		} else {
			r.b = r.b[:n:n]
		}
		r.t = typeBString
	case ':': // Integers
		if r.i, err = parseInt(b); err != nil {
			return
		}
		r.t = typeInteger
	case '*': // Arrays
		if n, err = parseLen(b); err != nil {
			return
		}
		if n < 0 {
			r.t = typeNil // the same as a nil bulk string, RESP3 has a single null
			return
		}
		if depth >= maxDepth {
			return ErrProtocol
		}
		if depth == 0 && c.br.safe >= 0 {
			c.br.safe = c.br.r // header parsed and not retained
		}
		if r.array == nil || cap(r.array) < n {
			r.array = make([]Reply, 0, min(n, maxArrayPrealloc))
		} else {
			r.unhide(n) // the elements left are not reused
		}
		r.hide = uint32(n) // n <= maxLen
		for i := 0; i < n; i++ {
			if i < cap(r.array) {
				// reuse the element, its nested array keeps its capacity.
				// Reset guarantees every element within cap is clean.
				r.array = r.array[:i+1]
			} else {
				r.array = append(r.array, Reply{})
			}
			if err = c.read(&r.array[i], depth+1); err != nil {
				return
			}
		}
		r.t = typeArray
	case '-': // Errors
		if len(b) == 0 {
			r.b = emptyBytes
		} else {
			r.b = b
		}
		r.t = typeError
	default:
		err = ErrProtocol
	}
	return
}

// do is Do with the scratch reply of c, which saves the sync.Pool round
// trip. The caller must call done if do succeeds.
func (c *Conn) do(cmd string, args []any) (*Reply, error) {
	if c == nil {
		return nil, ErrClosed // see PoolConn.Close
	}
	if err := c.roundtrip(cmd, args, &c.reply); err != nil {
		c.done(false, err)
		return nil, err
	}
	return &c.reply, nil
}

// recvScratch receives a reply into the scratch reply of c, mirroring do.
// The caller must call done if recvScratch succeeds.
func (c *Conn) recvScratch() (*Reply, error) {
	if c == nil {
		return nil, ErrClosed // see PoolConn.Close
	}
	if err := c.recv(&c.reply); err != nil {
		c.done(false, err)
		return nil, err
	}
	return &c.reply, nil
}

// done releases the scratch reply. Unless exposed, its space in the read
// buffer is reused, so the caller must have copied what it returns.
// A non-empty RedisErr aliases the buffer and always exposes the reply.
func (c *Conn) done(exposed bool, err error) {
	if re, ok := err.(RedisErr); (ok && len(re) > 0) || exposed {
		c.br.expose()
	}
	c.reply.release()
}

// DoNoReply wraps Do() and Reply.Err()
func (c *Conn) DoNoReply(cmd string, args ...any) error {
	r, err := c.do(cmd, args)
	if err != nil {
		return err
	}
	err = r.Err()
	c.done(false, err)
	return err
}

// DoOK sends cmd and reports whether Redis replied "+OK" (true) or nil (false).
// Useful for conditional commands such as SET key val NX.
func (c *Conn) DoOK(cmd string, args ...any) (bool, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return false, err
	}
	ok, err := r.ok()
	c.done(false, err)
	return ok, err
}

// DoBytes wraps Do() and Reply.Bytes().
// The result aliases the read buffer, see Conn; use DoString for a copy.
func (c *Conn) DoBytes(cmd string, args ...any) ([]byte, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	b, err := r.Bytes()
	c.done(len(b) > 0, err)
	return b, err
}

// DoString wraps Do() and Reply.String(), copying the result into a string.
// Unlike DoBytes, the result is safe to retain, and the Conn reuses the
// buffer space of the reply instead of allocating new buffers.
func (c *Conn) DoString(cmd string, args ...any) (string, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return "", err
	}
	s, err := r.String()
	c.done(false, err)
	return s, err
}

// DoBytesSlice wraps Do() and Reply.BytesSlice().
// Nil elements are nil, and a nil reply returns ErrNil like DoBytes.
// The elements alias the read buffer, see Conn.
func (c *Conn) DoBytesSlice(cmd string, args ...any) ([][]byte, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	bb, exposed, err := r.bytesSlice()
	c.done(exposed, err)
	return bb, err
}

// DoInteger wraps Do() and reply.Integer()
func (c *Conn) DoInteger(cmd string, args ...any) (int64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return 0, err
	}
	i, err := r.Integer()
	c.done(false, err)
	return i, err
}

// DoInt wraps Do() and Reply.Int().
func (c *Conn) DoInt(cmd string, args ...any) (int, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return 0, err
	}
	v, err := r.Int()
	c.done(false, err)
	return v, err
}

// DoInt64 wraps Do() and Reply.Int64().
func (c *Conn) DoInt64(cmd string, args ...any) (int64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return 0, err
	}
	v, err := r.Int64()
	c.done(false, err)
	return v, err
}

// DoUint64 wraps Do() and Reply.Uint64().
func (c *Conn) DoUint64(cmd string, args ...any) (uint64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return 0, err
	}
	v, err := r.Uint64()
	c.done(false, err)
	return v, err
}

// DoFloat64 wraps Do() and Reply.Float64().
func (c *Conn) DoFloat64(cmd string, args ...any) (float64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return 0, err
	}
	v, err := r.Float64()
	c.done(false, err)
	return v, err
}

// DoBool wraps Do() and Reply.Bool().
func (c *Conn) DoBool(cmd string, args ...any) (bool, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return false, err
	}
	v, err := r.Bool()
	c.done(false, err)
	return v, err
}

// DoStrings wraps Do() and Reply.Strings().
func (c *Conn) DoStrings(cmd string, args ...any) ([]string, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	v, err := r.Strings()
	c.done(false, err)
	return v, err
}

// DoInt64s wraps Do() and Reply.Int64s().
func (c *Conn) DoInt64s(cmd string, args ...any) ([]int64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	v, err := r.Int64s()
	c.done(false, err)
	return v, err
}

// DoFloat64s wraps Do() and Reply.Float64s().
func (c *Conn) DoFloat64s(cmd string, args ...any) ([]float64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	v, err := r.Float64s()
	c.done(false, err)
	return v, err
}

// DoBools wraps Do() and Reply.Bools().
func (c *Conn) DoBools(cmd string, args ...any) ([]bool, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	v, err := r.Bools()
	c.done(false, err)
	return v, err
}

// DoStringMap wraps Do() and Reply.StringMap().
func (c *Conn) DoStringMap(cmd string, args ...any) (map[string]string, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	v, err := r.StringMap()
	c.done(false, err)
	return v, err
}

// DoInt64Map wraps Do() and Reply.Int64Map().
func (c *Conn) DoInt64Map(cmd string, args ...any) (map[string]int64, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	v, err := r.Int64Map()
	c.done(false, err)
	return v, err
}

// DoScan executes a SCAN/HSCAN/SSCAN/ZSCAN command and returns the next cursor
// and the page of elements.
func (c *Conn) DoScan(cmd string, args ...any) (uint64, []string, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return 0, nil, err
	}
	cursor, items, err := r.scanResult()
	c.done(false, err)
	return cursor, items, err
}

// Scan iterates over keys returned by Redis SCAN, starting from cursor 0.
// Extra arguments such as "MATCH", pattern, "COUNT", n, or "TYPE", t are
// forwarded to each SCAN call.
func (c *Conn) Scan(args ...any) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		c.scan(args, yield)
	}
}

func (c *Conn) scan(args []any, yield func(string, error) bool) {
	var cursor uint64
	var keys []string
	for {
		next, page, err := c.scanPage(cursor, args, keys)
		if err != nil {
			yield("", err)
			return
		}
		keys = page
		for _, k := range keys {
			if !yield(k, nil) {
				return
			}
		}
		clear(keys)
		if next == 0 {
			return
		}
		cursor = next
	}
}

func (c *Conn) scanPage(cursor uint64, args []any, dst []string) (uint64, []string, error) {
	r, err := c.do("SCAN", []any{cursor, flatArgs(args)})
	if err != nil {
		return 0, nil, err
	}
	next, items, err := r.scanResultInto(dst)
	c.done(false, err)
	return next, items, err
}

// RecvNoReply receives the next reply and returns Reply.Err().
func (c *Conn) RecvNoReply() error {
	r, err := c.recvScratch()
	if err != nil {
		return err
	}
	err = r.Err()
	c.done(false, err)
	return err
}

// RecvOK receives the next reply and reports whether Redis replied "+OK" (true)
// or nil (false).
func (c *Conn) RecvOK() (bool, error) {
	r, err := c.recvScratch()
	if err != nil {
		return false, err
	}
	ok, err := r.ok()
	c.done(false, err)
	return ok, err
}

// RecvBytes receives the next reply and returns Reply.Bytes().
// The returned slice aliases the read buffer of c, see Conn.
func (c *Conn) RecvBytes() ([]byte, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	b, err := r.Bytes()
	c.done(len(b) > 0, err)
	return b, err
}

// RecvString receives the next reply and copies it into a string.
func (c *Conn) RecvString() (string, error) {
	r, err := c.recvScratch()
	if err != nil {
		return "", err
	}
	s, err := r.String()
	c.done(false, err)
	return s, err
}

// RecvInteger receives the next reply and returns Reply.Integer().
func (c *Conn) RecvInteger() (int64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return 0, err
	}
	v, err := r.Integer()
	c.done(false, err)
	return v, err
}

// RecvInt receives the next reply and returns Reply.Int().
func (c *Conn) RecvInt() (int, error) {
	r, err := c.recvScratch()
	if err != nil {
		return 0, err
	}
	v, err := r.Int()
	c.done(false, err)
	return v, err
}

// RecvInt64 receives the next reply and returns Reply.Int64().
func (c *Conn) RecvInt64() (int64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return 0, err
	}
	v, err := r.Int64()
	c.done(false, err)
	return v, err
}

// RecvUint64 receives the next reply and returns Reply.Uint64().
func (c *Conn) RecvUint64() (uint64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return 0, err
	}
	v, err := r.Uint64()
	c.done(false, err)
	return v, err
}

// RecvFloat64 receives the next reply and returns Reply.Float64().
func (c *Conn) RecvFloat64() (float64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return 0, err
	}
	v, err := r.Float64()
	c.done(false, err)
	return v, err
}

// RecvBool receives the next reply and returns Reply.Bool().
func (c *Conn) RecvBool() (bool, error) {
	r, err := c.recvScratch()
	if err != nil {
		return false, err
	}
	v, err := r.Bool()
	c.done(false, err)
	return v, err
}

// RecvBytesSlice receives the next reply and returns Reply.BytesSlice().
// The returned slices alias the read buffer of c, see Conn.
func (c *Conn) RecvBytesSlice() ([][]byte, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	bb, exposed, err := r.bytesSlice()
	c.done(exposed, err)
	return bb, err
}

// RecvStrings receives the next reply and returns Reply.Strings().
func (c *Conn) RecvStrings() ([]string, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	v, err := r.Strings()
	c.done(false, err)
	return v, err
}

// RecvInt64s receives the next reply and returns Reply.Int64s().
func (c *Conn) RecvInt64s() ([]int64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	v, err := r.Int64s()
	c.done(false, err)
	return v, err
}

// RecvFloat64s receives the next reply and returns Reply.Float64s().
func (c *Conn) RecvFloat64s() ([]float64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	v, err := r.Float64s()
	c.done(false, err)
	return v, err
}

// RecvBools receives the next reply and returns Reply.Bools().
func (c *Conn) RecvBools() ([]bool, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	v, err := r.Bools()
	c.done(false, err)
	return v, err
}

// RecvStringMap receives the next reply and returns Reply.StringMap().
func (c *Conn) RecvStringMap() (map[string]string, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	v, err := r.StringMap()
	c.done(false, err)
	return v, err
}

// RecvInt64Map receives the next reply and returns Reply.Int64Map().
func (c *Conn) RecvInt64Map() (map[string]int64, error) {
	r, err := c.recvScratch()
	if err != nil {
		return nil, err
	}
	v, err := r.Int64Map()
	c.done(false, err)
	return v, err
}

// RecvScan receives the next reply and unpacks it into dest using Reply.Scan.
// Unlike Recv, it reuses the scratch reply and read buffer of c; byte slices
// scanned into *[]byte and *[][]byte are copied and safe to retain.
func (c *Conn) RecvScan(dest ...any) error {
	r, err := c.recvScratch()
	if err != nil {
		return err
	}
	err = r.Scan(dest...)
	c.done(false, err)
	return err
}

// Session states set by commands. A Pool closes a conn in any of them
// instead of handing it to the next caller.
const (
	stateMulti   uint8 = 1 << iota // after MULTI, until EXEC or DISCARD
	stateWatch                     // after WATCH, until EXEC, DISCARD or UNWATCH
	stateChanged                   // after SELECT, CLIENT, ..., for good
	statePush                      // after a pub/sub command or MONITOR, for good
)

// track updates the session state of c for cmd with args, which expand to
// n bulk strings, or rejects cmd. It runs on
// every Send, so it rejects most commands on their length, inline, and
// first letter.
//
// States are set as soon as a command is sent, but only cleared by the
// successful reply of EXEC or DISCARD without args in a transaction, or
// of UNWATCH without args that was not queued in one. Otherwise the state
// stays and the conn is not reused.
//
// A pub/sub command or MONITOR puts c in statePush, or fails for Do.
// CLIENT and SCRIPT are tracked by their subcommand.
func (c *Conn) track(cmd string, n int, args []any, do bool) error {
	if len(cmd) >= 4 && len(cmd) <= 12 {
		return c.track4(cmd, n, args, do)
	}
	return nil
}

func (c *Conn) track4(cmd string, n int, args []any, do bool) error {
	switch cmd[0] | 0x20 { // lower case
	case 'a':
		c.change(cmd, "AUTH", "ASKING")
	case 'c':
		if is(cmd, "CLIENT") {
			return c.client(args)
		}
	case 'd':
		if is(cmd, "DISCARD") {
			c.endMulti(n)
		}
	case 'e':
		if is(cmd, "EXEC") {
			c.endMulti(n)
		}
	case 'h':
		c.change(cmd, "HELLO")
	case 'm':
		if is(cmd, "MULTI") {
			c.state |= stateMulti
			c.keep(stateMulti) // a pending end would not cover this transaction
		} else if is(cmd, "MONITOR") {
			return c.push(do)
		}
	case 'p', 's', 'u':
		if isAny(cmd, "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE",
			"UNSUBSCRIBE", "PUNSUBSCRIBE", "SUNSUBSCRIBE") {
			return c.push(do)
		} else if is(cmd, "SELECT") {
			c.state |= stateChanged
		} else if is(cmd, "SCRIPT") {
			if sub, _ := nthArg(args, 0); is(sub, "DEBUG") {
				c.state |= stateChanged // scripts of the conn run in the debugger
			}
		} else if is(cmd, "UNWATCH") && n == 0 {
			// run, not queued, it shows the transaction ended too
			c.endOnSuccess(stateMulti | stateWatch)
		}
	case 'q':
		c.change(cmd, "QUIT")
	case 'r':
		c.change(cmd, "RESET", "READONLY", "READWRITE")
	case 'w':
		if is(cmd, "WATCH") {
			c.state |= stateWatch
			c.keep(stateWatch) // a pending end would not cover this watch
		}
	}
	return nil
}

// push handles a command after which redis pushes messages without being
// asked: the pending count no longer tells which reply belongs to Do.
func (c *Conn) push(do bool) error {
	if do {
		return errPush
	}
	c.state |= statePush
	return nil
}

// client tracks CLIENT by its subcommand. The read-only ones leave the
// session alone. REPLY OFF and SKIP are rejected, since replies would no
// longer match commands. The others, like SETNAME or TRACKING, change it.
func (c *Conn) client(args []any) error {
	sub, _ := nthArg(args, 0)
	switch {
	case isAny(sub, "ID", "INFO", "LIST", "GETNAME", "GETREDIR", "TRACKINGINFO"):
	case is(sub, "REPLY"):
		if mode, _ := nthArg(args, 1); !is(mode, "ON") {
			return errClientReply
		}
	default:
		c.state |= stateChanged
	}
	return nil
}

// nthArg returns the i-th bulk string args expand to, if it is a string.
func nthArg(args []any, i int) (string, bool) {
	s, _, ok := nthArgAt(args, i, 0)
	return s, ok
}

func nthArgAt(args []any, i, depth int) (string, int, bool) {
	for _, a := range args {
		switch v := a.(type) {
		case string:
			if i == 0 {
				return v, 0, true
			}
			i--
		case []byte:
			if i == 0 {
				return ss(v), 0, true
			}
			i--
		case []string:
			if i < len(v) {
				return v[i], 0, true
			}
			i -= len(v)
		case [][]byte:
			if i < len(v) {
				return ss(v[i]), 0, true
			}
			i -= len(v)
		case []int:
			if i < len(v) {
				return "", -1, false
			}
			i -= len(v)
		case []int64:
			if i < len(v) {
				return "", -1, false
			}
			i -= len(v)
		case []uint64:
			if i < len(v) {
				return "", -1, false
			}
			i -= len(v)
		case []float64:
			if i < len(v) {
				return "", -1, false
			}
			i -= len(v)
		case flatArgs:
			val, rem, ok := nthArgAt(v, i, depth)
			if ok || rem < 0 {
				return val, rem, ok
			}
			i = rem
		case []any:
			if depth >= maxArgDepth {
				return "", -1, false
			}
			val, rem, ok := nthArgAt(v, i, depth+1)
			if ok || rem < 0 {
				return val, rem, ok
			}
			i = rem
		case map[string]string:
			if i < 2*len(v) {
				return "", -1, false
			}
			i -= 2 * len(v)
		case map[string][]byte:
			if i < 2*len(v) {
				return "", -1, false
			}
			i -= 2 * len(v)
		case map[string]any:
			if i < 2*len(v) {
				return "", -1, false
			}
			i -= 2 * len(v)
		default:
			if i == 0 {
				return "", -1, false
			}
			i--
		}
	}
	return "", i, false
}

// endMulti handles EXEC or DISCARD with n args, which end a transaction
// and the watches before it. Outside of a transaction, or with args, redis
// rejects them and keeps the watches.
func (c *Conn) endMulti(n int) {
	if n == 0 && c.state&stateMulti != 0 {
		c.endOnSuccess(stateMulti | stateWatch)
	}
}

// endOnSuccess makes the reply of the command being sent clear states,
// unless it is an error, as redis may still reject the command, without
// permission for it for example, or QUEUED. Only the last such command is
// followed: it shows the earlier ones ran, or the state stays.
func (c *Conn) endOnSuccess(states uint8) {
	c.ends = states
	c.endsIn = c.pd + 1 // the command is not counted yet
}

// keep makes the pending end, if any, leave states set by the command being
// sent, which it would not cover.
func (c *Conn) keep(states uint8) {
	if c.ends &^= states; c.ends == 0 {
		c.endsIn = 0
	}
}

// change marks the session of c as changed if cmd is one of names.
func (c *Conn) change(cmd string, names ...string) {
	if isAny(cmd, names...) {
		c.state |= stateChanged
	}
}

// isAny reports whether cmd is one of names, ignoring case.
func isAny(cmd string, names ...string) bool {
	for _, name := range names {
		if is(cmd, name) {
			return true
		}
	}
	return false
}

// is reports whether cmd is name, ignoring case.
func is(cmd, name string) bool {
	return len(cmd) == len(name) && strings.EqualFold(cmd, name)
}
