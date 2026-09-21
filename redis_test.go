package redisgo

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
	// comment it for package manage tools friendly
	// goredis "github.com/go-redis/redis"
	// redigo "github.com/gomodule/redigo/redis"
)

func TestRedis(t *testing.T) {
	conn, err := net.Dial("tcp", "127.0.0.1:6379")
	if err != nil {
		t.Skip("redis not available:", err)
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
	if !resp.IsNil() || resp.Err() != ErrNil {
		t.Fatal("expect nil reply", resp)
	}
	resp.Free()

	_, err = cli.DoBytes("XXX", "mykey")
	if err == nil {
		t.Fatal("nil")
	}
	if _, ok := err.(RedisErr); !ok {
		t.Fatal("expect RedisErr, got", err)
	}

	if bb, err = cli.DoBytesSlice("MGET", "x", "y"); err != nil || len(bb) != 2 {
		t.Fatal(bb, err)
	}
	if cli.Err() != nil {
		t.Fatal(cli.Err())
	}
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

// failConn fails every write, replies are never needed.
type failConn struct{ FakeConn }

func (c *failConn) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestFlushErrorIsFatal(t *testing.T) {
	c := NewConn(&failConn{}, WithReadTimeout(0), WithWriteTimeout(0))
	if err := c.Send("PING"); err != nil {
		t.Fatal(err)
	}
	err := c.Flush()
	if err == nil || err.Error() != "write failed" {
		t.Fatal(err)
	}
	if c.Err() != err {
		t.Fatalf("Flush error not recorded: %v", c.Err())
	}
	if !c.closed {
		t.Fatal("connection not closed after fatal write error")
	}
	if err := c.Send("PING"); err != c.Err() {
		t.Fatal(err)
	}
	if err := c.Flush(); err != c.Err() {
		t.Fatal(err)
	}
	if err := c.Close(); err != errClosed {
		t.Fatal(err)
	}
}

func TestCloseFlushes(t *testing.T) {
	sink := new(bytes.Buffer)
	c := NewConn(&FakeConn{}, WithReadTimeout(0), WithWriteTimeout(0))
	c.bw.Reset(sink)
	if err := c.Send("QUIT"); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if sink.String() != "*1\r\n$4\r\nQUIT\r\n" {
		t.Fatalf("Close did not flush: %q", sink.String())
	}
	if err := c.Close(); err != errClosed {
		t.Fatal(err)
	}
	if err := c.Send("PING"); err != errClosed {
		t.Fatal(err)
	}
}

// deadlineConn counts deadline updates.
type deadlineConn struct {
	chunkConn
	rd, wd int
}

func (c *deadlineConn) SetReadDeadline(time.Time) error  { c.rd++; return nil }
func (c *deadlineConn) SetWriteDeadline(time.Time) error { c.wd++; return nil }

// TestDeadlinesOnlyWhenTouchingNetwork checks that pipelined Sends do not
// arm the write deadline while they only fill the buffer.
func TestDeadlinesOnlyWhenTouchingNetwork(t *testing.T) {
	dc := &deadlineConn{chunkConn: chunkConn{data: []byte(strings.Repeat("+OK\r\n", 10))}}
	c := NewConn(dc, WithReadTimeout(time.Second), WithWriteTimeout(time.Second))
	var r Reply
	for i := 0; i < 10; i++ {
		if err := c.Send("SET", "k", "v"); err != nil {
			t.Fatal(err)
		}
	}
	if dc.wd != 0 {
		t.Fatalf("expect no write deadline while buffering, got %d", dc.wd)
	}
	for i := 0; i < 10; i++ {
		if err := c.Recv(&r); err != nil {
			t.Fatal(err)
		}
	}
	if dc.wd != 1 || dc.rd != 10 {
		t.Fatalf("expect 1 write and 10 read deadlines, got %d and %d", dc.wd, dc.rd)
	}
	// a command larger than the buffer must arm the write deadline itself
	if err := c.Send("SET", "k", strings.Repeat("v", 4096)); err != nil {
		t.Fatal(err)
	}
	if dc.wd != 2 {
		t.Fatalf("expect write deadline for an oversized command, got %d", dc.wd)
	}
}
