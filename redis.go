package redisgo

import (
	"bufio"
	"net"
	"strconv"
	"time"
	"unsafe"
)

// Conn represents a redis client
type Conn struct {
	conn net.Conn
	br   *reader
	bw   *bufio.Writer

	err    error
	closed bool
	pd     int

	rtimeout time.Duration
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
	r.bw = bufio.NewWriterSize(conn, o.wbuf)
	r.rtimeout = o.rtimeout
	r.wtimeout = o.wtimeout
	return &r
}

// SetReadTimeout sets the read timeout of underlying connection
func (c *Conn) SetReadTimeout(t time.Duration) {
	c.rtimeout = t
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

// Do sends command to redis and recv reply.
// Reply.Free() SHOULD be called when no longer used
func (c *Conn) Do(cmd string, args ...interface{}) (*Reply, error) {
	if err := c.Send(cmd, args...); err != nil {
		return nil, err
	}
	reply := replyPool.Get().(*Reply)
	if err := c.Recv(reply); err != nil {
		reply.Free()
		return nil, err
	}
	return reply, nil
}

// Send sends command to redis
func (c *Conn) Send(cmd string, args ...interface{}) (err error) {
	if err = c.Err(); err != nil {
		return
	}
	for _, a := range args {
		if !validarg(a) {
			return c.seterr(errInvalidArgType)
		}
	}
	c.pd++
	if c.wtimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
	}
	cc := commandPool.Get().(*command)
	err = c.seterr(cc.Reset(cmd).Args(args...).Dump(c.bw))
	commandPool.Put(cc)
	return
}

// Flush writes any buffered data to redis
func (c *Conn) Flush() error {
	if c.bw.Buffered() == 0 {
		return nil
	}
	if c.wtimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
	}
	return c.bw.Flush()
}

// Recv receives reply from redis
func (c *Conn) Recv(reply *Reply) (err error) {
	reply.Reset()
	if err = c.Err(); err != nil {
		return
	}
	if c.bw.Buffered() > 0 {
		if c.wtimeout > 0 {
			c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
		}
		err = c.seterr(c.bw.Flush())
		if err != nil {
			return
		}
	}
	c.pd--
	if c.rtimeout > 0 {
		c.conn.SetReadDeadline(time.Now().Add(c.rtimeout))
	}
	return c.seterr(c.read(reply))
}

// Conn returns the underlying net.Conn
func (c *Conn) Conn() net.Conn {
	return c.conn
}

// Close closes the underlying connection
func (c *Conn) Close() error {
	c.Flush()
	if c.closed {
		return errClosed
	}
	if c.err == nil {
		c.err = errClosed
	}
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

func (c *Conn) read(r *Reply) (err error) {
	var b []byte
	b, err = c.br.Readline()
	if err != nil {
		return
	}
	b = b[:len(b)-2] // remove CRLF
	if len(b) == 0 {
		err = errProtocol
		return
	}
	t := b[0]
	b = b[1:]
	var n int // for strlen or arraylen
	switch t {
	case '+': // Simple Strings
		r.b = b
		r.t = typeSString
	case '$': // Bulk Strings
		n, err = strconv.Atoi(ss(b))
		if err != nil {
			return
		}
		if n == -1 {
			r.t = typeNil
			return
		}
		r.b, err = c.br.Read(n + 2)
		if err != nil {
			return
		}
		r.b = r.b[:n]
		r.t = typeBString
	case ':': // Integers
		r.i, err = strconv.ParseInt(ss(b), 10, 64)
		if err == nil {
			r.t = typeInteger
		}
	case '*': // Arrays
		n, err = strconv.Atoi(ss(b))
		if err != nil {
			return
		}
		if n == -1 {
			r.t = typeNilArray
			return
		}
		if r.array == nil {
			r.array = make([]Reply, 0, n)
		}
		for i := 0; i < n; i++ {
			r.array = append(r.array, Reply{})
			if err = c.read(&r.array[i]); err != nil {
				return
			}
		}
		r.t = typeArray
	case '-': // Errors
		r.t = typeError
		r.err = *(*RedisErr)(unsafe.Pointer(&b))
	default:
		err = errProtocol
	}
	return
}

// DoNoReply wraps Do() and Reply.Err()
func (c *Conn) DoNoReply(cmd string, args ...interface{}) error {
	reply, err := c.Do(cmd, args...)
	if err != nil {
		return err
	}
	defer reply.Free()
	return reply.Err()
}

// DoBytes wraps Do() and Reply.Bytes()
func (c *Conn) DoBytes(cmd string, args ...interface{}) ([]byte, error) {
	reply, err := c.Do(cmd, args...)
	if err != nil {
		return nil, err
	}
	defer reply.Free()
	return reply.Bytes()
}

// DoBytesSlice wraps Do() and reply.Array() and reply.Bytes()
func (c *Conn) DoBytesSlice(cmd string, args ...interface{}) ([][]byte, error) {
	reply, err := c.Do(cmd, args...)
	if err != nil {
		return nil, err
	}
	defer reply.Free()
	aa, err := reply.Array()
	if err != nil {
		return nil, err
	}
	ret := make([][]byte, 0, len(aa))
	for i := range aa {
		if aa[i].IsNil() {
			ret = append(ret, []byte(nil))
			continue
		}
		b, err := aa[i].Bytes()
		if err != nil {
			return nil, err
		}
		ret = append(ret, b)
	}
	return ret, nil
}

// DoInt wraps Do() and reply.Integer()
func (c *Conn) DoInteger(cmd string, args ...interface{}) (int64, error) {
	reply, err := c.Do(cmd, args...)
	if err != nil {
		return 0, err
	}
	defer reply.Free()
	return reply.Integer()
}
