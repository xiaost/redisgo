package redisgo

import (
	"io"
	"net"
	"sync"
	"testing"
	"time"
	// comment it for package manage tools friendly
	// goredis "github.com/go-redis/redis"
	// redigo "github.com/gomodule/redigo/redis"
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

	b, err := cli.DoBytes("GET", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "myvalue" {
		t.Fatal(string(b))
	}

	bb, err := cli.DoBytesSlice("MGET", "mykey", "notexistskey")
	if err != nil {
		t.Fatal(err)
	}
	if len(bb) != 2 {
		t.Fatal(len(bb))
	}
	if string(bb[0]) != "myvalue" {
		t.Fatal(string(bb[0]))
	}
	if bb[1] != nil {
		t.Fatal(string(bb[1]))
	}

	time.Sleep(55 * time.Millisecond)

	resp, err := cli.Do("GET", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsNil() && resp.Err() == ErrNil {
		t.Fatal("not nil", resp)
	}
	resp.Free()

	_, err = cli.DoBytes("XXX", "mykey")
	if err == nil {
		t.Fatal("nil")
	}
	t.Log(err)

	cli.Do("MGET", "x", "y")
}

func BenchmarkCmdSetRedisgo(b *testing.B) {
	b.ReportAllocs()
	var conn = net.Conn(&FakeConn{reply: []byte("+OK\r\n")})
	cli := NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0))
	var reply Reply
	for i := 0; i < b.N; i++ {
		key := "key"
		val := "value"
		if err := cli.Send("SET", key, val, "EX", i); err != nil {
			b.Fatal(err)
		}
		cli.Flush()
		if err := cli.Recv(&reply); err != nil {
			b.Fatal(err)
		}
		_, _ = reply.Bytes()
	}
}

/* comment it for package manage tools friendly
func BenchmarkCmdSetRedigo(b *testing.B) {
	b.ReportAllocs()
	var conn = net.Conn(&FakeConn{reply: []byte("+OK\r\n")})
	cli := redigo.NewConn(conn, 0, 0)
	for i := 0; i < b.N; i++ {
		key := "key"
		val := "value"
		if err := cli.Send("SET", key, val, "EX", i); err != nil {
			b.Fatal(err)
		}
		cli.Flush()
		_, err := redigo.Bytes(cli.Receive())
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCmdSetGoredis(b *testing.B) {
	b.ReportAllocs()
	opts := goredis.Options{
		Dialer: func() (net.Conn, error) {
			return &FakeConn{reply: []byte("+OK\r\n")}, nil
		},
	}
	cli := goredis.NewClient(&opts)
	for i := 0; i < b.N; i++ {
		key := "key"
		val := "value"
		if err := cli.Set(key, val, time.Duration(i)*time.Second).Err(); err != nil {
			b.Fatal(err)
		}
	}
}
*/

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
