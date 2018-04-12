package redisgo

import (
	"bufio"
	"net"
	"strconv"
	"sync"
	"time"
)

// Redis represents a redis client
type Redis struct {
	mu sync.RWMutex

	conn net.Conn
	br   *reader
	bw   *bufio.Writer

	err    error
	closed bool

	rtimeout time.Duration
	wtimeout time.Duration
}

// NewRedis creates Redis
func NewRedis(conn net.Conn, ops ...Option) *Redis {
	o := defaultoptions
	for _, op := range ops {
		o = op(o)
	}
	var r Redis
	r.conn = conn
	r.br = newReader(conn, o.rbuf)
	r.bw = bufio.NewWriterSize(conn, o.wbuf)
	r.rtimeout = o.rtimeout
	r.wtimeout = o.wtimeout
	return &r
}

// SetReadTimeout sets the read timeout of underlying connection
func (c *Redis) SetReadTimeout(t time.Duration) {
	c.mu.Lock()
	c.rtimeout = t
	c.mu.Unlock()
}

// SetWriteTimeout sets the write timeout of underlying connection
func (c *Redis) SetWriteTimeout(t time.Duration) {
	c.mu.Lock()
	c.wtimeout = t
	c.mu.Unlock()
}

// SetTimeout sets the read and write timeouts of underlying connection
func (c *Redis) SetTimeout(t time.Duration) {
	c.SetReadTimeout(t)
	c.SetWriteTimeout(t)
}

// Do sends command to redis and recv reply
// Reply.Free() SHOULD be called when no longer used
func (c *Redis) Do(cmd string, args ...interface{}) (*Reply, error) {
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
func (c *Redis) Send(cmd string, args ...interface{}) (err error) {
	if err = c.Err(); err != nil {
		return
	}
	for _, a := range args {
		if !validarg(a) {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.seterr(errInvalidArgType)
		}
	}
	cc := commandPool.Get().(*command)
	defer commandPool.Put(cc)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wtimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
	}
	return c.seterr(cc.Reset(cmd).Args(args...).Dump(c.bw))
}

// Flush writes any buffered data to redis
func (c *Redis) Flush() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wtimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
	}
	return c.bw.Flush()
}

// Recv receives reply from redis
func (c *Redis) Recv(reply *Reply) (err error) {
	reply.Reset()
	if err = c.Err(); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bw.Buffered() > 0 {
		if c.wtimeout > 0 {
			c.conn.SetWriteDeadline(time.Now().Add(c.wtimeout))
		}
		err = c.seterr(c.bw.Flush())
		if err != nil {
			return
		}
	}
	if c.rtimeout > 0 {
		c.conn.SetReadDeadline(time.Now().Add(c.rtimeout))
	}
	return c.seterr(c.read(reply))
}

// Conn returns the underlying net.Conn
func (c *Redis) Conn() net.Conn {
	return c.conn
}

// Close closes the underlying connection
func (c *Redis) Close() error {
	c.Flush()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errClosed
	}
	if c.err == nil {
		c.err = errClosed
	}
	c.closed = true
	return c.conn.Close()
}

// Err returns last fatal err
// if not nil, caller must not reuse the instance
func (c *Redis) Err() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

func (c *Redis) seterr(err error) error {
	if err == nil {
		return nil
	}
	if c.err == nil {
		c.err = err
	}
	if !c.closed {
		c.closed = true
		c.conn.Close()
	}
	return err
}

func (c *Redis) read(r *Reply) (err error) {
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
	var n int // for strlen or arraylen
	switch b[0] {
	case '+': // Simple Strings
		r.b = b[1:]
		r.t = replySString
	case '$': // Bulk Strings
		n, err = strconv.Atoi(ss(b[1:]))
		if err != nil {
			return
		}
		if n == -1 {
			r.t = replyNil
			return
		}
		r.b, err = c.br.Read(n + 2)
		if err != nil {
			return
		}
		r.b = r.b[:n]
		r.t = replyBString
	case ':': // Integers
		r.i, err = strconv.ParseInt(ss(b[1:]), 10, 64)
		if err == nil {
			r.t = replyInteger
		}
	case '*': // Arrays
		n, err = strconv.Atoi(ss(b[1:]))
		if err != nil {
			return
		}
		if n == -1 {
			r.t = replyArray
			return
		}
		if r.array == nil {
			r.array = make([]Reply, 0, n)
		}
		for i := 0; i < n; i++ {
			e := Reply{}
			if err = c.read(&e); err != nil {
				return
			}
			r.array = append(r.array, e)
		}
	case '-': // Errors
		r.t = replyError
		r.err = RedisErr(b[1:])
	default:
		err = errProtocol
	}
	return
}
