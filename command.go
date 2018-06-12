package redisgo

import (
	"io"
	"strconv"
	"sync"
	"unsafe"
)

const (
	cmdbufsz = 1024
)

type command struct {
	n   int
	buf []byte
	tmp [1 + 24 + 2]byte // '*' or '$' + max-float-len + CRLF
}

var commandPool = sync.Pool{
	New: func() interface{} {
		return &command{buf: make([]byte, 0, cmdbufsz)}
	},
}

func (c *command) Reset(cmd string) *command {
	c.n = 0
	c.buf = c.buf[:0]
	c.appends(cmd)
	return c
}

func (c *command) Dump(w io.Writer) (err error) {
	if c.n == 0 {
		panic("command without args")
	}
	p := append(c.tmp[:0], '*')
	p = strconv.AppendInt(p, int64(c.n), 10)
	p = append(p, CR, LF)
	_, err = w.Write(p)
	if err == nil {
		_, err = w.Write(c.buf)
	}
	return
}

func (c *command) appendi(i int64) {
	c.appends(ss(strconv.AppendInt(c.tmp[:0], i, 10)))
}

func (c *command) appendf(f float64) {
	c.appends(ss(strconv.AppendFloat(c.tmp[:0], f, 'g', -1, 64)))
}

func (c *command) appends(s string) {
	c.buf = append(c.buf, '$')
	c.buf = strconv.AppendInt(c.buf, int64(len(s)), 10)
	c.buf = append(c.buf, CR, LF)
	c.buf = append(c.buf, s...)
	c.buf = append(c.buf, CR, LF)
	c.n++
}

// Args append args to the command.
// type must be one of:
// int, int8, int16, int32, int64
// uint, uint8, uint16, uint32, uint64
// float32, float64
// []byte, string, []string
func (c *command) Args(aa ...interface{}) *command {
	for _, a := range aa {
		switch v := a.(type) {
		case int:
			c.appendi(int64(v))
		case int8:
			c.appendi(int64(v))
		case int16:
			c.appendi(int64(v))
		case int32:
			c.appendi(int64(v))
		case int64:
			c.appendi(int64(v))
		case uint:
			c.appendi(int64(v))
		case uint8:
			c.appendi(int64(v))
		case uint16:
			c.appendi(int64(v))
		case uint32:
			c.appendi(int64(v))
		case uint64:
			c.appendi(int64(v))
		case float32:
			c.appendf(float64(v))
		case float64:
			c.appendf(float64(v))
		case []byte:
			// fix issue: https://github.com/golang/go/issues/15730
			p := uintptr(unsafe.Pointer(&v))
			c.appends(ss(*(*[]byte)(unsafe.Pointer(p))))
		case string:
			p := uintptr(unsafe.Pointer(&v))
			c.appends(*(*string)(unsafe.Pointer(p)))
		case []string:
			for _, s := range v {
				p := uintptr(unsafe.Pointer(&s))
				c.appends(*(*string)(unsafe.Pointer(p)))
			}
		default:
			panic("unknown args type")
		}
	}
	return c
}
