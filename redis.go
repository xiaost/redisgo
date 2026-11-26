package redisgo

import (
	"bufio"
	"net"
	"strconv"
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
	conn  net.Conn
	br    *reader
	bw    *bufio.Writer
	cmd   []byte // scratch for commands larger than bw
	reply Reply  // scratch for the DoXxx helpers, see do

	err    error
	closed bool
	pd     int

	wtimeout time.Duration
}

// NewConn creates Conn
func NewConn(conn net.Conn, ops ...Option) *Conn {
	o := defaultoptions
	for _, op := range ops {
		o = op(o)
	}
	var r Conn
	r.conn = conn
	r.br = newReader(conn, o.rbuf)
	r.br.timeout = o.rtimeout
	r.bw = bufio.NewWriterSize(conn, o.wbuf)
	r.wtimeout = o.wtimeout
	return &r
}

// SetReadTimeout sets the read timeout of underlying connection
func (c *Conn) SetReadTimeout(t time.Duration) {
	c.br.timeout = t
}

// SetWriteTimeout sets the write timeout of underlying connection
func (c *Conn) SetWriteTimeout(t time.Duration) {
	c.wtimeout = t
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
	c.br.expose() // reply aliases the read buffer from now on
	return reply, nil
}

// roundtrip sends a command and reads its reply into reply. Replies still
// pending from earlier Sends are read into reply and dropped first, and the
// first RedisErr among them is returned after the reply of the command has
// been read, so the conn stays in sync.
func (c *Conn) roundtrip(cmd string, args []any, reply *Reply) error {
	if err := c.Send(cmd, args...); err != nil {
		return err
	}
	var pending error
	for c.pd > 1 {
		if err := c.recv(reply); err != nil {
			return err
		}
		if pending == nil && reply.t == typeError {
			pending = RedisErr(reply.b)
			c.br.expose() // pending aliases the read buffer
		}
	}
	if err := c.recv(reply); err != nil {
		return err
	}
	return pending
}

// Send buffers a command for redis. Nothing is written to the network until
// Flush or Recv is called or the write buffer fills up.
// Send returns errInvalidArgType without touching the connection if an arg
// has an unsupported type.
func (c *Conn) Send(cmd string, args ...any) error {
	if err := c.Err(); err != nil {
		return err
	}
	n, size, err := argsInfo(args)
	if err != nil {
		return err
	}
	c.pd++
	if cmdSize(cmd, size) <= c.bw.Available() {
		// The command fits: encode it straight into the spare capacity of
		// the writer. Nothing is flushed, so no write deadline is needed.
		_, err = c.bw.Write(appendCommand(c.bw.AvailableBuffer(), cmd, n, args))
		return c.seterr(err)
	}
	// bufio flushes while writing a command that does not fit.
	if c.wtimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
	}
	c.cmd = appendCommand(c.cmd[:0], cmd, n, args)
	_, err = c.bw.Write(c.cmd)
	if cap(c.cmd) > maxCmdBuf {
		c.cmd = nil // do not pin a huge scratch buffer to the conn
	}
	return c.seterr(err)
}

// Flush writes any buffered data to redis
func (c *Conn) Flush() error {
	if err := c.Err(); err != nil {
		return err
	}
	if c.bw.Buffered() == 0 {
		return nil
	}
	if c.wtimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
	}
	return c.seterr(c.bw.Flush())
}

// Recv receives reply from redis
func (c *Conn) Recv(reply *Reply) error {
	err := c.recv(reply)
	c.br.expose() // reply aliases the read buffer from now on
	return err
}

// recv is Recv without exposing reply: the buffer space of reply is reused
// by later replies unless c.br.expose is called before the next recv.
func (c *Conn) recv(reply *Reply) error {
	if err := c.Flush(); err != nil {
		return err // reply is left alone
	}
	reply.Reset()
	c.pd--
	c.br.reclaim()
	c.br.disarm() // the read timeout covers this reply only
	if err := c.read(reply, 0); err != nil {
		return c.seterr(err)
	}
	c.br.drop()
	return nil
}

// Conn returns the underlying net.Conn
func (c *Conn) Conn() net.Conn {
	return c.conn
}

// Close flushes buffered commands and closes the underlying connection.
// It returns errClosed if the connection was closed already, by Close or
// by an earlier fatal error.
func (c *Conn) Close() error {
	if c.closed {
		return errClosed
	}
	if err := c.Flush(); err != nil {
		return err // Flush closed the connection already
	}
	c.err = errClosed
	c.closed = true
	return c.conn.Close()
}

// Err returns last fatal err.
// if not nil, caller must not reuse the instance
func (c *Conn) Err() error {
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
			return 0, errProtocol
		}
		return n, nil
	}
	d := b
	if len(d) > 0 && (d[0] == '-' || d[0] == '+') {
		d = d[1:]
	}
	if len(d) == 0 {
		return 0, errProtocol
	}
	var n int64
	for _, c := range d {
		if c < '0' || c > '9' {
			return 0, errProtocol
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
		return 0, errProtocol
	}
	n, err := parseInt(b)
	if err != nil || n < -1 || n > maxLen {
		return 0, errProtocol
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
		return errProtocol
	}
	t := b[0]
	b = b[1:]
	var n int // for strlen or arraylen
	switch t {
	case '+': // Simple Strings
		r.b = b
		r.t = typeSString
	case '$': // Bulk Strings
		if n, err = parseLen(b); err != nil {
			return
		}
		if n < 0 {
			r.t = typeNil
			return
		}
		if r.b, err = c.br.Read(n + 2); err != nil {
			return
		}
		if r.b[n] != CR || r.b[n+1] != LF {
			return errProtocol
		}
		r.b = r.b[:n:n]
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
			return errProtocol
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
		r.b = b
		r.t = typeError
	default:
		err = errProtocol
	}
	return
}

// do is Do with the scratch reply of c, which saves the sync.Pool round
// trip. The caller must call done if do succeeds.
func (c *Conn) do(cmd string, args []any) (*Reply, error) {
	if err := c.roundtrip(cmd, args, &c.reply); err != nil {
		c.done(false, err)
		return nil, err
	}
	return &c.reply, nil
}

// done releases the scratch reply. Unless exposed, its space in the read
// buffer is reused, so the caller must have copied what it returns.
// A RedisErr aliases the buffer and always exposes the reply.
func (c *Conn) done(exposed bool, err error) {
	if _, ok := err.(RedisErr); ok || exposed {
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

// DoBytes wraps Do() and Reply.Bytes().
// The result aliases the read buffer, see Conn; use DoString for a copy.
func (c *Conn) DoBytes(cmd string, args ...any) ([]byte, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	b, err := r.Bytes()
	c.done(true, err)
	return b, err
}

// DoString wraps Do() and Reply.Bytes(), and copies the result into a string.
// Unlike DoBytes, the result is safe to retain, and the Conn reuses the
// buffer space of the reply instead of allocating new buffers.
func (c *Conn) DoString(cmd string, args ...any) (string, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return "", err
	}
	b, err := r.Bytes()
	s := string(b)
	c.done(false, err)
	return s, err
}

// DoBytesSlice wraps Do() and reply.Array() and reply.Bytes().
// Nil elements are nil, and a nil reply returns ErrNil like DoBytes.
// The elements alias the read buffer, see Conn.
func (c *Conn) DoBytesSlice(cmd string, args ...any) ([][]byte, error) {
	r, err := c.do(cmd, args)
	if err != nil {
		return nil, err
	}
	bb, err := r.bytesSlice()
	c.done(true, err)
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
