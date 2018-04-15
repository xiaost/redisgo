package redisgo

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

func TestRedis(t *testing.T) {
	conn, err := net.Dial("tcp", "127.0.0.1:6379")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := NewConn(conn)
	if err := cli.Send("SET", "mykey", "myvalue", "PX", 50); err != nil {
		t.Fatal(err)
	}
	var reply Reply
	if err := cli.Recv(&reply); err != nil {
		t.Fatal(err)
	}
	if !reply.IsOK() {
		t.Fatal("set not ok")
	}

	resp, err := cli.Do("GET", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := resp.Bytes(); string(s) != "myvalue" {
		t.Fatal(s)
	}
	resp.Free()

	time.Sleep(55 * time.Millisecond)

	resp, err = cli.Do("GET", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsNil() && resp.Err() == ErrNil {
		t.Fatal("not nil", resp)
	}
	resp.Free()

	resp, err = cli.Do("XXX", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err() == nil {
		t.Fatal("resp err == nil")
	}
	t.Log(resp.Err())

	cli.Do("MGET", "x", "y")
}

func BenchmarkCmdSet(b *testing.B) {
	var conn = net.Conn(&FakeConn{reply: []byte("+OK\r\n")})
	cli := NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0))
	var reply Reply
	for i := 0; i < b.N; i++ {
		key := "key"
		val := "value"
		if err := cli.Send("SET", key, val, "PX", i); err != nil {
			b.Fatal(err)
		}
		cli.Flush()
		if err := cli.Recv(&reply); err != nil {
			b.Fatal(err)
		}
		_, _ = reply.Bytes()
	}
}

type FakeConn struct {
	mu     sync.Mutex
	reply  []byte
	r      int
	closed bool
}

func (c *FakeConn) Read(b []byte) (int, error) {
	if c.closed {
		return 0, errClosed
	}
	if len(c.reply) == 0 {
		return 0, io.EOF
	}
	if c.r >= len(c.reply) {
		c.r = 0
	}
	n := copy(b, c.reply[c.r:])
	c.r += n
	return n, nil
}

type noaddr struct{}

func (noaddr) Network() string { return "noaddr" }
func (noaddr) String() string  { return "noaddr" }

func (c *FakeConn) Write(b []byte) (int, error) { return len(b), nil }
func (c *FakeConn) Close() error                { c.closed = true; return nil }
func (c *FakeConn) LocalAddr() net.Addr         { return noaddr{} }
func (c *FakeConn) RemoteAddr() net.Addr        { return noaddr{} }

func (c *FakeConn) SetDeadline(time.Time) error      { return nil }
func (c *FakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *FakeConn) SetWriteDeadline(time.Time) error { return nil }
