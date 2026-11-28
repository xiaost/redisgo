package redisgo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
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

	// Test ergonomic helpers against real Redis.
	const prefix = "redisgo:ci:"
	if err := cli.DoNoReply("DEL", prefix+"ok", prefix+"num", prefix+"hash", prefix+"scan1", prefix+"scan2"); err != nil {
		t.Fatal(err)
	}
	ok, err := cli.DoOK("SET", prefix+"ok", "1", "NX", "EX", 10)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	ok, err = cli.DoOK("SET", prefix+"ok", "2", "NX", "EX", 10)
	if err != nil || ok {
		t.Fatal(ok, err)
	}
	if n, err := cli.DoInt64("INCRBY", prefix+"num", 5); err != nil || n != 5 {
		t.Fatal(n, err)
	}
	if n, err := cli.DoInt64("GET", prefix+"num"); err != nil || n != 5 {
		t.Fatal(n, err)
	}
	if f, err := cli.DoFloat64("INCRBYFLOAT", prefix+"num", 0.5); err != nil || f != 5.5 {
		t.Fatal(f, err)
	}
	if exists, err := cli.DoBool("EXISTS", prefix+"num"); err != nil || !exists {
		t.Fatal(exists, err)
	}
	if err := cli.DoNoReply("HSET", prefix+"hash", map[string]any{"a": "1", "b": 2}); err != nil {
		t.Fatal(err)
	}
	if m, err := cli.DoStringMap("HGETALL", prefix+"hash"); err != nil || m["a"] != "1" || m["b"] != "2" {
		t.Fatal(m, err)
	}
	if err := cli.DoNoReply("MSET", map[string]string{prefix + "scan1": "1", prefix + "scan2": "2"}); err != nil {
		t.Fatal(err)
	}
	var scanned []string
	for k, err := range cli.Scan("MATCH", prefix+"scan*", "COUNT", 10) {
		if err != nil {
			t.Fatal(err)
		}
		scanned = append(scanned, k)
	}
	if len(scanned) != 2 {
		t.Fatalf("expect 2 scanned keys, got %v", scanned)
	}
	sc := NewScript(1, "return redis.call('GET', KEYS[1])")
	r, err := sc.DoConn(cli, prefix+"num")
	if err != nil {
		t.Fatal(err)
	}
	var scVal float64
	if err := r.Scan(&scVal); err != nil || scVal != 5.5 {
		t.Fatal(scVal, err)
	}
	r.Free()
	_ = cli.DoNoReply("DEL", prefix+"ok", prefix+"num", prefix+"hash", prefix+"scan1", prefix+"scan2")
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
		return 0, ErrClosed
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
	if err := c.Close(); err != ErrClosed {
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
	if err := c.Close(); err != ErrClosed {
		t.Fatal(err)
	}
	if err := c.Send("PING"); err != ErrClosed {
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
// arm the write deadline while they only fill the buffer, and that Recv
// does not arm the read deadline for a reply that is buffered already.
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
	// all 10 replies arrive with the first read
	if dc.wd != 1 || dc.rd != 1 {
		t.Fatalf("expect 1 write and 1 read deadline, got %d and %d", dc.wd, dc.rd)
	}
	// one reply per read: every Recv has to arm the read deadline
	dc2 := &deadlineConn{chunkConn: chunkConn{data: []byte(strings.Repeat("+OK\r\n", 10)), chunk: 5}}
	c2 := NewConn(dc2, WithReadTimeout(time.Second), WithWriteTimeout(time.Second))
	for i := 0; i < 10; i++ {
		if err := c2.DoNoReply("SET", "k", "v"); err != nil {
			t.Fatal(err)
		}
	}
	if dc2.rd != 10 {
		t.Fatalf("expect 10 read deadlines, got %d", dc2.rd)
	}
	// a command larger than the buffer must arm the write deadline itself
	if err := c.Send("SET", "k", strings.Repeat("v", 4096)); err != nil {
		t.Fatal(err)
	}
	if dc.wd != 2 {
		t.Fatalf("expect write deadline for an oversized command, got %d", dc.wd)
	}
}

// TestReclaimKeepsExposedBytes mixes helpers that expose the read buffer
// with helpers that let the Conn reuse it, and checks that nothing handed
// out to the caller is ever overwritten.
func TestReclaimKeepsExposedBytes(t *testing.T) {
	type check func(t *testing.T)
	for _, chunk := range []int{0, 1, 3, 7, 64} {
		var resp strings.Builder
		var steps []func(c *Conn) check
		for i := 0; i < 300; i++ {
			v := fmt.Sprintf("v%d-%s", i, strings.Repeat("x", i%40))
			if i%50 == 49 {
				v = strings.Repeat("L", 70<<10) // larger than maxReuseBuf
			}
			switch i % 7 {
			case 0:
				resp.WriteString(tstr(v))
				steps = append(steps, func(c *Conn) check {
					b, err := c.DoBytes("GET", "k")
					if err != nil {
						t.Fatal(err)
					}
					return func(t *testing.T) {
						if string(b) != v {
							t.Fatalf("DoBytes %d corrupted: %.20q", i, b)
						}
					}
				})
			case 1:
				resp.WriteString(tstr(v))
				steps = append(steps, func(c *Conn) check {
					if s, err := c.DoString("GET", "k"); err != nil || s != v {
						t.Fatalf("DoString %d: %.20q %v", i, s, err)
					}
					return nil
				})
			case 2:
				resp.WriteString(":" + strconv.Itoa(i) + "\r\n")
				steps = append(steps, func(c *Conn) check {
					if n, err := c.DoInteger("INCR", "k"); err != nil || n != int64(i) {
						t.Fatalf("DoInteger %d: %d %v", i, n, err)
					}
					return nil
				})
			case 3:
				resp.WriteString("-ERR " + v + "\r\n")
				steps = append(steps, func(c *Conn) check {
					err := c.DoNoReply("SET", "k", "v")
					if _, ok := err.(RedisErr); !ok {
						t.Fatalf("DoNoReply %d: expect RedisErr, got %v", i, err)
					}
					return func(t *testing.T) {
						if err.Error() != "ERR "+v {
							t.Fatalf("RedisErr %d corrupted: %.20q", i, err.Error())
						}
					}
				})
			case 4:
				resp.WriteString("+OK\r\n")
				steps = append(steps, func(c *Conn) check {
					if err := c.DoNoReply("SET", "k", "v"); err != nil {
						t.Fatalf("DoNoReply %d: %v", i, err)
					}
					return nil
				})
			case 5:
				resp.WriteString(tcmd(tstr(v), "$-1\r\n"))
				steps = append(steps, func(c *Conn) check {
					bb, err := c.DoBytesSlice("MGET", "a", "b")
					if err != nil || len(bb) != 2 || bb[1] != nil {
						t.Fatalf("DoBytesSlice %d: %v %v", i, len(bb), err)
					}
					return func(t *testing.T) {
						if string(bb[0]) != v {
							t.Fatalf("DoBytesSlice %d corrupted: %.20q", i, bb[0])
						}
					}
				})
			case 6:
				resp.WriteString(tstr(v))
				steps = append(steps, func(c *Conn) check {
					r, err := c.Do("GET", "k")
					if err != nil {
						t.Fatal(err)
					}
					b, _ := r.Bytes()
					r.Free()
					return func(t *testing.T) {
						if string(b) != v {
							t.Fatalf("Do %d corrupted: %.20q", i, b)
						}
					}
				})
			}
		}
		c := NewConn(&chunkConn{data: []byte(resp.String()), chunk: chunk},
			WithReadBuffer(0), WithReadTimeout(0), WithWriteTimeout(0))
		var checks []check
		for _, step := range steps {
			if ck := step(c); ck != nil {
				checks = append(checks, ck)
			}
			for _, ck := range checks {
				ck(t)
			}
		}
	}
}

// TestHelpersReuseReadBuffer checks that helpers which copy their result
// read every reply into the same buffer instead of allocating new ones.
func TestHelpersReuseReadBuffer(t *testing.T) {
	cases := []struct {
		reply string
		do    func(c *Conn) error
	}{
		{"+OK\r\n", func(c *Conn) error { return c.DoNoReply("SET", "k", "v") }},
		{":12345\r\n", func(c *Conn) error { _, err := c.DoInteger("INCR", "k"); return err }},
		{"$5\r\nhello\r\n", func(c *Conn) error {
			s, err := c.DoString("GET", "k")
			if err == nil && s != "hello" {
				err = errors.New(s)
			}
			return err
		}},
	}
	for _, tc := range cases {
		c := NewConn(&FakeConn{reply: []byte(tc.reply)}, WithReadTimeout(0), WithWriteTimeout(0))
		buf := &c.br.b[0]
		allocs := testing.AllocsPerRun(10000, func() {
			if err := tc.do(c); err != nil {
				t.Fatal(err)
			}
		})
		if &c.br.b[0] != buf {
			t.Errorf("%q: read buffer replaced", tc.reply)
		}
		// DoString allocates its result only
		if want := float64(strings.Count(tc.reply, "hello")); allocs != want {
			t.Errorf("%q: expect %v allocs, got %v", tc.reply, want, allocs)
		}
	}
}

func TestDoBytesSliceNilArray(t *testing.T) {
	c := newTestConn("*-1\r\n*0\r\n", 0)
	if bb, err := c.DoBytesSlice("BLPOP", "k", 1); bb != nil || err != ErrNil {
		t.Fatal(bb, err)
	}
	if bb, err := c.DoBytesSlice("MGET", "k"); bb == nil || len(bb) != 0 || err != nil {
		t.Fatal(bb, err)
	}
}

func TestDoReadsPendingReplies(t *testing.T) {
	c := newTestConn("+OK\r\n+QUEUED\r\n+QUEUED\r\n*2\r\n:1\r\n:2\r\n", 3)
	c.Send("MULTI")
	c.Send("INCR", "a")
	c.Send("INCR", "b")
	r, err := c.Do("EXEC")
	if err != nil {
		t.Fatal(err)
	}
	if aa, err := r.Array(); err != nil || len(aa) != 2 || aa[1].i != 2 {
		t.Fatalf("expect the reply of EXEC, got %v %v", aa, err)
	}
	r.Free()
	if c.pd != 0 {
		t.Fatal(c.pd)
	}
}

func TestDoReturnsPendingError(t *testing.T) {
	for _, chunk := range []int{0, 1, 3} {
		testDoReturnsPendingError(t, chunk)
	}
}

func testDoReturnsPendingError(t *testing.T, chunk int) {
	c := newTestConn("+OK\r\n-ERR first\r\n-ERR second\r\n$1\r\nx\r\n$1\r\ny\r\n:7777777\r\n", chunk)
	c.Send("SET", "a", 1)
	c.Send("SET", "b", 2)
	c.Send("SET", "c", 3)
	_, err := c.DoString("GET", "x")
	if _, ok := err.(RedisErr); !ok || err.Error() != "ERR first" {
		t.Fatalf("expect the first pending error, got %v", err)
	}
	// the conn is still in sync, and err still intact after later reads
	if s, err := c.DoString("GET", "y"); err != nil || s != "y" {
		t.Fatal(s, err)
	}
	if n, err := c.DoInteger("INCR", "n"); err != nil || n != 7777777 {
		t.Fatal(n, err)
	}
	if err.Error() != "ERR first" {
		t.Fatalf("pending error corrupted: %q", err.Error())
	}
	if c.pd != 0 || c.Err() != nil {
		t.Fatal(c.pd, c.Err())
	}
}

// TestReadBufferSizes checks that values the read buffer can be reused for
// allocate nothing but the result once warm, and that a large reply does
// not keep its buffer attached to the conn.
func TestReadBufferSizes(t *testing.T) {
	for _, size := range []int{4 << 10, 8 << 10, 60 << 10, 65500, 64 << 10} {
		v := strings.Repeat("x", size)
		c := NewConn(&FakeConn{reply: []byte(tstr(v))}, WithReadTimeout(0), WithWriteTimeout(0))
		c.DoString("GET", "k")
		allocs := testing.AllocsPerRun(50, func() {
			if s, err := c.DoString("GET", "k"); err != nil || len(s) != size {
				t.Fatal(len(s), err)
			}
		})
		if allocs != 1 {
			t.Errorf("%d bytes: expect 1 alloc for the string, got %v", size, allocs)
		}
	}
	big := strings.Repeat("x", 1<<20)
	// FakeConn never reads past the end of a reply, so the buffer drains
	c := NewConn(&FakeConn{reply: []byte(tstr(big))}, WithReadTimeout(0), WithWriteTimeout(0))
	b, err := c.DoBytes("GET", "k")
	if err != nil || len(c.br.b) > maxReuseBuf {
		t.Fatalf("buffer of %d kept after a large reply: %v", len(c.br.b), err)
	}
	if s, err := c.DoString("GET", "k"); err != nil || s != big || len(c.br.b) > maxReuseBuf {
		t.Fatalf("buffer of %d kept after a large reply: %v", len(c.br.b), err)
	}
	if string(b) != big {
		t.Fatal("bytes of DoBytes corrupted")
	}
}

// TestDoReusesNestedArrays checks that the helpers, which reset their reply
// once when done and again before the next read, keep reusing the arrays
// nested in replies of the same shape, like GEOPOS.
func TestDoReusesNestedArrays(t *testing.T) {
	pos := make([]string, 64) // within the read buffer, which is not reused otherwise
	for i := range pos {
		pos[i] = tcmd(tstr("1.5"), tstr("2.5"))
	}
	c := NewConn(&FakeConn{reply: []byte(tcmd(pos...))}, WithReadTimeout(0), WithWriteTimeout(0))
	c.DoNoReply("GEOPOS", "k")
	allocs := testing.AllocsPerRun(20, func() {
		if err := c.DoNoReply("GEOPOS", "k"); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("expect 0 allocs, got %v", allocs)
	}
}

func TestConnDoAndRecvHelpers(t *testing.T) {
	resp := "+OK\r\n" +
		"$-1\r\n" +
		":42\r\n" +
		tstr("100") +
		tstr("200") +
		tstr("12.5") +
		":1\r\n" +
		tcmd(tstr("a"), tstr("b")) +
		tcmd(":1\r\n", tstr("2")) +
		tcmd(tstr("1.5"), ":2\r\n") +
		tcmd(":1\r\n", tstr("false")) +
		tcmd(tstr("k"), tstr("v")) +
		tcmd(tstr("c"), ":9\r\n") +
		tcmd(tstr("5"), tcmd(tstr("k1")))

	c := newTestConn(resp, 0)
	if ok, err := c.DoOK("SET", "k", "v"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := c.DoOK("SET", "k", "v", "NX"); err != nil || ok {
		t.Fatal(ok, err)
	}
	if v, err := c.DoInt("INCR", "k"); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if v, err := c.DoInt64("GET", "k"); err != nil || v != 100 {
		t.Fatal(v, err)
	}
	if v, err := c.DoUint64("GET", "k"); err != nil || v != 200 {
		t.Fatal(v, err)
	}
	if v, err := c.DoFloat64("ZSCORE", "z", "m"); err != nil || v != 12.5 {
		t.Fatal(v, err)
	}
	if v, err := c.DoBool("EXISTS", "k"); err != nil || !v {
		t.Fatal(v, err)
	}
	if v, err := c.DoStrings("LRANGE", "l", 0, -1); err != nil || len(v) != 2 || v[1] != "b" {
		t.Fatal(v, err)
	}
	if v, err := c.DoInt64s("SMISMEMBER", "s", "a", "b"); err != nil || len(v) != 2 || v[1] != 2 {
		t.Fatal(v, err)
	}
	if v, err := c.DoFloat64s("X"); err != nil || len(v) != 2 || v[0] != 1.5 || v[1] != 2 {
		t.Fatal(v, err)
	}
	if v, err := c.DoBools("X"); err != nil || len(v) != 2 || !v[0] || v[1] {
		t.Fatal(v, err)
	}
	if v, err := c.DoStringMap("HGETALL", "h"); err != nil || v["k"] != "v" {
		t.Fatal(v, err)
	}
	if v, err := c.DoInt64Map("HGETALL", "h"); err != nil || v["c"] != 9 {
		t.Fatal(v, err)
	}
	if cur, items, err := c.DoScan("SCAN", 0); err != nil || cur != 5 || len(items) != 1 || items[0] != "k1" {
		t.Fatal(cur, items, err)
	}
}

func TestConnNewHelpersNoAlloc(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		fn    func(c *Conn) error
		alloc float64
	}{
		{"DoOK", "+OK\r\n", func(c *Conn) error { _, err := c.DoOK("SET", "k", "v", "NX"); return err }, 0},
		{"DoInt64", "$3\r\n123\r\n", func(c *Conn) error { _, err := c.DoInt64("GET", "k"); return err }, 0},
		{"DoUint64", "$3\r\n123\r\n", func(c *Conn) error { _, err := c.DoUint64("GET", "k"); return err }, 0},
		{"DoFloat64", "$4\r\n1.25\r\n", func(c *Conn) error { _, err := c.DoFloat64("ZSCORE", "z", "m"); return err }, 0},
		{"DoBool", ":1\r\n", func(c *Conn) error { _, err := c.DoBool("EXISTS", "k"); return err }, 0},
		{"RecvNoReply", "+OK\r\n", func(c *Conn) error { c.Send("SET", "k", "v"); return c.RecvNoReply() }, 0},
		{"RecvOK", "+OK\r\n", func(c *Conn) error { c.Send("SET", "k", "v"); _, err := c.RecvOK(); return err }, 0},
		{"RecvInt64", "$3\r\n123\r\n", func(c *Conn) error { c.Send("GET", "k"); _, err := c.RecvInt64(); return err }, 0},
		{"RecvFloat64", "$4\r\n1.25\r\n", func(c *Conn) error { c.Send("GET", "k"); _, err := c.RecvFloat64(); return err }, 0},
		{"RecvBool", ":1\r\n", func(c *Conn) error { c.Send("EXISTS", "k"); _, err := c.RecvBool(); return err }, 0},
		{"RecvString", "$5\r\nhello\r\n", func(c *Conn) error { c.Send("GET", "k"); _, err := c.RecvString(); return err }, 1},
	}
	for _, tc := range cases {
		c := NewConn(&FakeConn{reply: []byte(tc.reply)}, WithReadTimeout(0), WithWriteTimeout(0))
		buf := &c.br.b[0]
		allocs := testing.AllocsPerRun(200, func() {
			if err := tc.fn(c); err != nil {
				t.Fatal(tc.name, err)
			}
		})
		if &c.br.b[0] != buf {
			t.Errorf("%s: read buffer replaced instead of reused", tc.name)
		}
		if allocs != tc.alloc {
			t.Errorf("%s: expect %v allocs, got %v", tc.name, tc.alloc, allocs)
		}
	}
}

func TestConnScanIter(t *testing.T) {
	// Multi-page scan with an empty intermediate page, then early break, then error.
	resp := tcmd(tstr("7"), tcmd(tstr("k1"), tstr("k2"))) +
		tcmd(tstr("9"), "*0\r\n") +
		tcmd(tstr("0"), tcmd(tstr("k3"))) +
		tcmd(tstr("5"), tcmd(tstr("a"), tstr("b"))) +
		"-ERR scan failed\r\n"

	c := newTestConn(resp, 0)
	var keys []string
	for k, err := range c.Scan("MATCH", "k*", "COUNT", 10) {
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if len(keys) != 3 || keys[0] != "k1" || keys[1] != "k2" || keys[2] != "k3" {
		t.Fatalf("unexpected keys: %v", keys)
	}

	// Early break stops iterating without sending extra commands.
	var early []string
	for k, err := range c.Scan() {
		if err != nil {
			t.Fatal(err)
		}
		early = append(early, k)
		break
	}
	if len(early) != 1 || early[0] != "a" {
		t.Fatalf("unexpected early keys: %v", early)
	}

	// Error is yielded cleanly.
	var gotErr error
	for _, err := range c.Scan() {
		gotErr = err
	}
	if gotErr == nil || gotErr.Error() != "ERR scan failed" {
		t.Fatalf("expect scan error, got %v", gotErr)
	}
}

type streamChunkConn struct {
	FakeConn
	chunk int
}

func (c *streamChunkConn) Read(b []byte) (int, error) {
	if c.chunk > 0 && len(b) > c.chunk {
		b = b[:c.chunk]
	}
	return c.FakeConn.Read(b)
}

// TestPipelineReadBufferCompactionNoAlloc checks that deep pipelines whose
// replies straddle the read buffer boundary reuse the buffer via in-place
// compaction instead of allocating new buffers.
func TestPipelineReadBufferCompactionNoAlloc(t *testing.T) {
	// 31-byte replies do not divide the 2048-byte read buffer evenly.
	reply := strings.Repeat("$24\r\n123456789012345678901234\r\n", 200)
	for _, chunk := range []int{7, 100, 1400} {
		c := NewConn(&streamChunkConn{FakeConn: FakeConn{reply: []byte(reply)}, chunk: chunk},
			WithReadTimeout(0), WithWriteTimeout(0))
		buf := &c.br.b[0]
		allocs := testing.AllocsPerRun(20, func() {
			for range 200 {
				if err := c.Send("GET", "k"); err != nil {
					t.Fatal(err)
				}
			}
			for range 200 {
				if err := c.RecvNoReply(); err != nil {
					t.Fatal(err)
				}
			}
		})
		if allocs != 0 {
			t.Errorf("chunk=%d: expect 0 allocs, got %v", chunk, allocs)
		}
		if &c.br.b[0] != buf {
			t.Errorf("chunk=%d: read buffer replaced instead of compacted", chunk)
		}
	}
}

// TestPipelineCompactionPreservesExposedBytes mixes pinned zero-copy replies
// with pipelined unexposed replies across multiple chunk sizes and verifies
// that compaction never overwrites exposed bytes.
func TestPipelineCompactionPreservesExposedBytes(t *testing.T) {
	for _, chunk := range []int{1, 7, 64, 1400} {
		var resp strings.Builder
		var want []string
		for i := range 200 {
			v := fmt.Sprintf("val-%03d-%s", i, strings.Repeat("x", i%31))
			resp.WriteString(tstr(v))
			want = append(want, v)
		}
		c := NewConn(&chunkConn{data: []byte(resp.String()), chunk: chunk},
			WithReadBuffer(minReadBufferSize), WithReadTimeout(0), WithWriteTimeout(0))
		for range 200 {
			if err := c.Send("GET", "k"); err != nil {
				t.Fatal(err)
			}
		}
		var exposed [][]byte
		var exposedIdx []int
		for i := range 200 {
			if i%17 == 0 {
				b, err := c.RecvBytes()
				if err != nil || string(b) != want[i] {
					t.Fatalf("chunk=%d i=%d: got %q %v", chunk, i, b, err)
				}
				exposed = append(exposed, b)
				exposedIdx = append(exposedIdx, i)
			} else {
				s, err := c.RecvString()
				if err != nil || s != want[i] {
					t.Fatalf("chunk=%d i=%d: got %q %v", chunk, i, s, err)
				}
			}
		}
		for j, b := range exposed {
			if string(b) != want[exposedIdx[j]] {
				t.Fatalf("chunk=%d idx=%d: exposed bytes corrupted: got %q want %q", chunk, exposedIdx[j], b, want[exposedIdx[j]])
			}
		}
	}
}

// TestUnexposedRepliesReuseReadBuffer checks that nil, empty, and integer
// replies do not pin the read buffer in DoBytes, DoBytesSlice, Do, and Recv.
func TestUnexposedRepliesReuseReadBuffer(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		step  func(c *Conn) error
	}{
		{"DoBytes/nil", "$-1\r\n", func(c *Conn) error {
			b, err := c.DoBytes("GET", "k")
			if err != ErrNil || b != nil {
				return fmt.Errorf("got %v %v", b, err)
			}
			return nil
		}},
		{"DoBytes/empty", "$0\r\n\r\n", func(c *Conn) error {
			b, err := c.DoBytes("GET", "k")
			if err != nil || b == nil || len(b) != 0 || cap(b) != 0 {
				return fmt.Errorf("got %#v cap=%d err=%v", b, cap(b), err)
			}
			_ = append(b, 'X') // must not mutate shared emptyBytes
			return nil
		}},
		{"DoBytesSlice/nil", "*-1\r\n", func(c *Conn) error {
			bb, err := c.DoBytesSlice("BLPOP", "k", 1)
			if err != ErrNil || bb != nil {
				return fmt.Errorf("got %v %v", bb, err)
			}
			return nil
		}},
		{"DoBytesSlice/all-nil-and-empty", "*2\r\n$-1\r\n$0\r\n\r\n", func(c *Conn) error {
			bb, err := c.DoBytesSlice("MGET", "a", "b")
			if err != nil || len(bb) != 2 || bb[0] != nil || bb[1] == nil || len(bb[1]) != 0 || cap(bb[1]) != 0 {
				return fmt.Errorf("got %#v %v", bb, err)
			}
			return nil
		}},
		{"Do/integer", ":12345\r\n", func(c *Conn) error {
			r, err := c.Do("INCR", "k")
			if err != nil {
				return err
			}
			n, err := r.Integer()
			r.Free()
			if err != nil || n != 12345 {
				return fmt.Errorf("got %d %v", n, err)
			}
			return nil
		}},
		{"Do/nil", "$-1\r\n", func(c *Conn) error {
			r, err := c.Do("GET", "k")
			if err != nil {
				return err
			}
			ok := r.IsNil()
			r.Free()
			if !ok {
				return errors.New("expected nil")
			}
			return nil
		}},
		{"Do/empty", "$0\r\n\r\n", func(c *Conn) error {
			r, err := c.Do("GET", "k")
			if err != nil {
				return err
			}
			b, err := r.Bytes()
			r.Free()
			if err != nil || b == nil || len(b) != 0 || cap(b) != 0 {
				return fmt.Errorf("got %#v %v", b, err)
			}
			return nil
		}},
		{"Recv/integer", ":99\r\n", func(c *Conn) error {
			var r Reply
			if err := c.Send("INCR", "k"); err != nil {
				return err
			}
			if err := c.Recv(&r); err != nil {
				return err
			}
			if n, err := r.Integer(); err != nil || n != 99 {
				return fmt.Errorf("got %d %v", n, err)
			}
			return nil
		}},
		{"DoNoReply/empty-err", "-\r\n", func(c *Conn) error {
			err := c.DoNoReply("BAD")
			re, ok := err.(RedisErr)
			if !ok || len(re) != 0 {
				return fmt.Errorf("got %#v", err)
			}
			return nil
		}},
	}
	for _, tc := range cases {
		c := NewConn(&FakeConn{reply: []byte(tc.reply)}, WithReadTimeout(0), WithWriteTimeout(0))
		buf := &c.br.b[0]
		for range 1000 {
			if err := tc.step(c); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		if &c.br.b[0] != buf || c.br.pin != 0 {
			t.Errorf("%s: read buffer pinned/replaced (reused=%v pin=%d)", tc.name, &c.br.b[0] == buf, c.br.pin)
		}
	}
}

func TestDoStringConstSimpleStringsNoAlloc(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{"+OK\r\n", "OK"},
		{"+PONG\r\n", "PONG"},
		{"+QUEUED\r\n", "QUEUED"},
	} {
		c := NewConn(&FakeConn{reply: []byte(tc.raw)}, WithReadTimeout(0), WithWriteTimeout(0))
		allocs := testing.AllocsPerRun(100, func() {
			s, err := c.DoString("PING")
			if err != nil || s != tc.want {
				t.Fatal(s, err)
			}
		})
		if allocs != 0 {
			t.Errorf("%q: expect 0 allocs, got %v", tc.raw, allocs)
		}
	}
}

func TestConnScanIterAllocsAndRetention(t *testing.T) {
	keys := make([]string, 30)
	for i := range keys {
		keys[i] = tstr("key:" + strconv.Itoa(1000+i))
	}
	scanResp := tcmd(tstr("1001"), tcmd(keys[:10]...)) +
		tcmd(tstr("2002"), "*0\r\n") +
		tcmd(tstr("3003"), tcmd(keys[10:20]...)) +
		tcmd(tstr("4004"), "*0\r\n") +
		tcmd(tstr("0"), tcmd(keys[20:30]...))

	c := NewConn(&FakeConn{reply: []byte(scanResp)}, WithReadTimeout(0), WithWriteTimeout(0))
	pat := "key:*"
	// Verify all keys retained across pages remain intact after internal slice clearing.
	var retained []string
	for k, err := range c.Scan("MATCH", pat, "COUNT", 10) {
		if err != nil {
			t.Fatal(err)
		}
		retained = append(retained, k)
	}
	if len(retained) != 30 || retained[0] != "key:1000" || retained[29] != "key:1029" {
		t.Fatalf("unexpected retained keys: len=%d first=%q last=%q", len(retained), retained[0], retained[len(retained)-1])
	}

	// 1 reused []string + 3 packed string buffers = 4 allocs (0 for cursor/callArgs/empty pages).
	allocs := testing.AllocsPerRun(50, func() {
		count := 0
		for k, err := range c.Scan("MATCH", pat, "COUNT", 10) {
			if err != nil {
				t.Fatal(err)
			}
			if len(k) == 0 {
				t.Fatal("empty key")
			}
			count++
		}
		if count != 30 {
			t.Fatal(count)
		}
	})
	if allocs != 4 {
		t.Fatalf("Conn.Scan(5 pages: 3x10 + 2 empty): expect 4 allocs, got %v", allocs)
	}

	// Empty-only scan: 0 allocs across pages.
	emptyResp := tcmd(tstr("500"), "*0\r\n") + tcmd(tstr("0"), "*0\r\n")
	cEmpty := NewConn(&FakeConn{reply: []byte(emptyResp)}, WithReadTimeout(0), WithWriteTimeout(0))
	allocs = testing.AllocsPerRun(50, func() {
		for _, err := range cEmpty.Scan("MATCH", pat, "COUNT", 10) {
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("Conn.Scan(empty pages): expect 0 allocs, got %v", allocs)
	}
}

func TestNewConnAllocsAndLayout(t *testing.T) {
	if off := unsafe.Offsetof(Conn{}.lease); off%8 != 0 {
		t.Fatalf("Conn.lease offset %d is not 8-byte aligned for 32-bit atomics", off)
	}
	if sz := unsafe.Sizeof(Reply{}); sz > 64 {
		t.Fatalf("Reply grew past 64 bytes: %d", sz)
	}
	fc := &FakeConn{}
	allocs := testing.AllocsPerRun(100, func() {
		_ = NewConn(fc)
	})
	if allocs > 3 {
		t.Fatalf("NewConn: expect <= 3 allocs (Conn + rbuf + wbuf), got %v", allocs)
	}
}

func TestScanNestedArgDepth(t *testing.T) {
	// Nested []any at maxArgDepth-1 (7) levels is valid in Do("SCAN", 0, deep)
	// and must also be accepted by Conn.Scan and Pool.Scan.
	deep := []any{"COUNT", 10}
	for range maxArgDepth - 1 {
		deep = []any{deep}
	}
	resp := tcmd(tstr("0"), tcmd(tstr("k1"), tstr("k2")))

	c := NewConn(&FakeConn{reply: []byte(resp)}, WithReadTimeout(0), WithWriteTimeout(0))
	var got []string
	for k, err := range c.Scan(deep) {
		if err != nil {
			t.Fatalf("Conn.Scan(depth %d): %v", maxArgDepth-1, err)
		}
		got = append(got, k)
	}
	if len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("Conn.Scan unexpected keys: %v", got)
	}

	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(resp)}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer p.Close()
	got = got[:0]
	for k, err := range p.Scan(context.Background(), deep) {
		if err != nil {
			t.Fatalf("Pool.Scan(depth %d): %v", maxArgDepth-1, err)
		}
		got = append(got, k)
	}
	if len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("Pool.Scan unexpected keys: %v", got)
	}

	// >= 8 args works identically through flatArgs.
	var captured bytes.Buffer
	wc := &captureWriteConn{FakeConn: FakeConn{reply: []byte(resp)}}
	c8 := NewConn(wc, WithReadTimeout(0), WithWriteTimeout(0))
	got = got[:0]
	for k, err := range c8.Scan("A", "1", "B", "2", "C", "3", "D", "4") {
		if err != nil {
			t.Fatalf("Conn.Scan(8 args): %v", err)
		}
		got = append(got, k)
	}
	captured.Write(appendCommand(nil, "SCAN", 9, []any{uint64(0), "A", "1", "B", "2", "C", "3", "D", "4"}))
	if len(got) != 2 || !bytes.Equal(wc.buf.Bytes(), captured.Bytes()) {
		t.Fatalf("Conn.Scan(8 args) mismatch: got=%v wire=%q want=%q", got, wc.buf.Bytes(), captured.Bytes())
	}

	// >= 8 args combined with a depth-(maxArgDepth-1) nested []any must still
	// succeed just like Do("SCAN", 0, ...).
	c8Deep := NewConn(&FakeConn{reply: []byte(resp)}, WithReadTimeout(0), WithWriteTimeout(0))
	got = got[:0]
	for k, err := range c8Deep.Scan("A", "1", "B", "2", "C", "3", "D", deep) {
		if err != nil {
			t.Fatalf("Conn.Scan(8 args, depth %d): %v", maxArgDepth-1, err)
		}
		got = append(got, k)
	}
	if len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("Conn.Scan(8 args, deep) unexpected keys: %v", got)
	}
	got = got[:0]
	for k, err := range p.Scan(context.Background(), "A", "1", "B", "2", "C", "3", "D", deep) {
		if err != nil {
			t.Fatalf("Pool.Scan(8 args, depth %d): %v", maxArgDepth-1, err)
		}
		got = append(got, k)
	}
	if len(got) != 2 || got[0] != "k1" || got[1] != "k2" {
		t.Fatalf("Pool.Scan(8 args, deep) unexpected keys: %v", got)
	}

	// Large >64KB SCAN arg exercises flatArgs through streaming writeCommand.
	bigPat := strings.Repeat("p", maxCmdBuf+1024)
	wcBig := &captureWriteConn{FakeConn: FakeConn{reply: []byte(resp)}}
	cBig := NewConn(wcBig, WithReadTimeout(0), WithWriteTimeout(0))
	got = got[:0]
	for k, err := range cBig.Scan("MATCH", bigPat, "COUNT", 1000) {
		if err != nil {
			t.Fatalf("Conn.Scan(>64KB pattern): %v", err)
		}
		got = append(got, k)
	}
	wantBig := appendCommand(nil, "SCAN", 5, []any{uint64(0), "MATCH", bigPat, "COUNT", 1000})
	if len(got) != 2 || !bytes.Equal(wcBig.buf.Bytes(), wantBig) {
		t.Fatalf("Conn.Scan(>64KB pattern) mismatch: len=%d gotLen=%d wantLen=%d", len(got), wcBig.buf.Len(), len(wantBig))
	}

	// Non-small int arg (>255) and stack-backed []byte pattern must not escape in Scan.
	emptyResp := tcmd(tstr("0"), "*0\r\n")
	cNoEsc := NewConn(&FakeConn{reply: []byte(emptyResp)}, WithReadTimeout(0), WithWriteTimeout(0))
	allocs := testing.AllocsPerRun(100, func() {
		var pat [8]byte
		copy(pat[:], "user:*")
		for _, err := range cNoEsc.Scan("MATCH", pat[:6], "COUNT", 1000) {
			if err != nil {
				t.Fatal(err)
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("Conn.Scan(stack []byte, COUNT 1000): expect 0 allocs, got %v", allocs)
	}
}

func TestDoBytesSliceLargeBufferGrowth(t *testing.T) {
	// Repeated 10KB DoBytesSlice replies should grow the read buffer past 16KB
	// instead of shrinking back to 2KB and reallocating 2KB+4KB+8KB on every call.
	keys := make([]string, 200)
	for i := range keys {
		keys[i] = tstr("session:01234567-89ab-cdef-0123-456789ab" + strconv.Itoa(1000+i))
	}
	fc := &FakeConn{reply: []byte(tcmd(keys...))}
	c := NewConn(fc, WithReadTimeout(0), WithWriteTimeout(0))
	for range 3 {
		bb, err := c.DoBytesSlice("MGET", "k")
		if err != nil || len(bb) != 200 {
			t.Fatal(len(bb), err)
		}
	}
	if len(c.br.b) < 16<<10 {
		t.Fatalf("DoBytesSlice(10KB) failed to grow read buffer past 16KB: len(br.b)=%d", len(c.br.b))
	}
	// Switching back to small exposed replies should return to r.sz once the
	// large buffer fills, rather than staying large forever.
	fc.reply = []byte(tcmd(tstr("small1"), tstr("small2")))
	fc.r = 0
	for range 2000 {
		if _, err := c.DoBytesSlice("MGET", "a", "b"); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.br.b) != defaultoptions.rbuf {
		t.Fatalf("small DoBytesSlice replies should shrink buffer back to %d, got %d", defaultoptions.rbuf, len(c.br.b))
	}
}

// TestReadBufferAdversarialAliasing fuzzes interleaved zero-copy (DoBytes,
// DoBytesSlice, Do, Recv, RecvBytes, RecvBytesSlice, RedisErr) and unexposed
// (DoString, DoStrings, DoNoReply, Pipeline.Drain) replies across adversarial
// chunk sizes, buffer sizes, empty/nil/integer/nested/oversized (>64KB) replies,
// verifying that compact/reclaimShift/grow/drop never overwrite any exposed bytes.
func TestReadBufferAdversarialAliasing(t *testing.T) {
	type opKind int
	const (
		opRecvBytes opKind = iota
		opRecvString
		opRecvNoReply
		opRecvReply
		opRecvBytesSlice
		opRecvStrings
		opRecvInt
		opRecvNil
		opRecvEmptyBytes
		opRecvErr
		opOversizedBytes
		opOversizedString
		opNestedReply
		opPendingErrDo
	)
	type step struct {
		kind  opKind
		wire  string
		want  string
		wants []string
	}

	bigPayload := strings.Repeat("Q", maxReuseBuf+512)
	bigPayload2 := strings.Repeat("W", maxReuseBuf+777)

	buildStream := func() ([]step, []byte) {
		var steps []step
		var wire strings.Builder
		add := func(s step) {
			steps = append(steps, s)
			wire.WriteString(s.wire)
		}
		for i := range 64 {
			tag := fmt.Sprintf("item-%03d-%s", i, strings.Repeat(string(rune('a'+i%26)), (i*13)%79+1))
			switch i % 14 {
			case 0:
				add(step{kind: opRecvBytes, wire: tstr(tag), want: tag})
			case 1:
				add(step{kind: opRecvString, wire: tstr(tag), want: tag})
			case 2:
				add(step{kind: opRecvNoReply, wire: "+OK\r\n"})
			case 3:
				add(step{kind: opRecvReply, wire: tstr(tag), want: tag})
			case 4:
				a1, a2 := "m0-"+tag, "m1-"+tag
				add(step{kind: opRecvBytesSlice, wire: tcmd(tstr(a1), "$-1\r\n", "$0\r\n\r\n", tstr(a2)), wants: []string{a1, "", "", a2}})
			case 5:
				a1, a2 := "s0-"+tag, "s1-"+tag
				add(step{kind: opRecvStrings, wire: tcmd(tstr(a1), tstr(a2), "+OK\r\n"), wants: []string{a1, a2, "OK"}})
			case 6:
				add(step{kind: opRecvInt, wire: ":" + strconv.Itoa(i*100+7) + "\r\n", want: strconv.Itoa(i*100 + 7)})
			case 7:
				add(step{kind: opRecvNil, wire: "$-1\r\n"})
			case 8:
				add(step{kind: opRecvEmptyBytes, wire: "$0\r\n\r\n"})
			case 9:
				msg := "ERR fail-" + tag
				add(step{kind: opRecvErr, wire: "-" + msg + "\r\n", want: msg})
			case 10:
				if i == 10 {
					add(step{kind: opOversizedBytes, wire: tstr(bigPayload), want: bigPayload})
				} else {
					add(step{kind: opRecvBytes, wire: tstr(tag), want: tag})
				}
			case 11:
				if i == 11 {
					add(step{kind: opOversizedString, wire: tstr(bigPayload2), want: bigPayload2})
				} else {
					add(step{kind: opRecvString, wire: "+" + tag + "\r\n", want: tag})
				}
			case 12:
				n1, n2 := "nest1-"+tag, "nest2-"+tag
				add(step{kind: opNestedReply, wire: tcmd(tcmd(tstr(n1), ":9\r\n"), tcmd("$-1\r\n", tstr(n2))), wants: []string{n1, n2}})
			case 13:
				msg := "ERR pend-" + tag
				add(step{kind: opPendingErrDo, wire: "-" + msg + "\r\n+OK\r\n", want: msg})
			}
		}
		return steps, []byte(wire.String())
	}

	steps, data := buildStream()
	for _, rbuf := range []int{minReadBufferSize, minReadBufferSize + 1, 256, 2048} {
		for _, chunk := range []int{1, 2, 3, 5, 7, 13, 64, 99, 100, 101, 500, 1400} {
			c := NewConn(&chunkConn{data: data, chunk: chunk},
				WithReadBuffer(rbuf), WithReadTimeout(0), WithWriteTimeout(0))

			type held struct {
				got  []byte
				want string
			}
			var pinned []held
			var replies []*Reply

			for idx, st := range steps {
				switch st.kind {
				case opRecvBytes:
					if err := c.Send("GET", "k"); err != nil {
						t.Fatal(err)
					}
					b, err := c.RecvBytes()
					if err != nil || string(b) != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: RecvBytes got %q %v", rbuf, chunk, idx, b, err)
					}
					pinned = append(pinned, held{b, st.want})
				case opRecvString:
					s, err := c.DoString("GET", "k")
					if err != nil || s != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoString got %q %v", rbuf, chunk, idx, s, err)
					}
				case opRecvNoReply:
					if err := c.DoNoReply("SET", "k", "v"); err != nil {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoNoReply %v", rbuf, chunk, idx, err)
					}
				case opRecvReply:
					r, err := c.Do("GET", "k")
					if err != nil {
						t.Fatalf("rbuf=%d chunk=%d step=%d: Do %v", rbuf, chunk, idx, err)
					}
					b, err := r.Bytes()
					if err != nil || string(b) != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: Do.Bytes got %q %v", rbuf, chunk, idx, b, err)
					}
					pinned = append(pinned, held{b, st.want})
					replies = append(replies, r)
				case opRecvBytesSlice:
					bb, err := c.DoBytesSlice("MGET", "a", "b", "c", "d")
					if err != nil || len(bb) != 4 || bb[1] != nil || bb[2] == nil || len(bb[2]) != 0 ||
						string(bb[0]) != st.wants[0] || string(bb[3]) != st.wants[3] {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoBytesSlice got %v %v", rbuf, chunk, idx, bb, err)
					}
					pinned = append(pinned, held{bb[0], st.wants[0]}, held{bb[3], st.wants[3]})
				case opRecvStrings:
					ss, err := c.DoStrings("MGET", "a", "b", "c")
					if err != nil || len(ss) != 3 || ss[0] != st.wants[0] || ss[1] != st.wants[1] || ss[2] != st.wants[2] {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoStrings got %v %v", rbuf, chunk, idx, ss, err)
					}
				case opRecvInt:
					n, err := c.DoInteger("INCR", "k")
					if err != nil || strconv.FormatInt(n, 10) != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoInteger got %d %v", rbuf, chunk, idx, n, err)
					}
				case opRecvNil:
					b, err := c.DoBytes("GET", "nil")
					if err != ErrNil || b != nil {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoBytes nil got %v %v", rbuf, chunk, idx, b, err)
					}
				case opRecvEmptyBytes:
					b, err := c.DoBytes("GET", "empty")
					if err != nil || b == nil || len(b) != 0 || cap(b) != 0 {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoBytes empty got %#v %v", rbuf, chunk, idx, b, err)
					}
				case opRecvErr:
					err := c.DoNoReply("BAD")
					re, ok := err.(RedisErr)
					if !ok || re.Error() != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: DoNoReply err got %v", rbuf, chunk, idx, err)
					}
					pinned = append(pinned, held{[]byte(re), st.want})
				case opOversizedBytes:
					b, err := c.DoBytes("GET", "big")
					if err != nil || string(b) != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: oversized DoBytes len=%d err=%v", rbuf, chunk, idx, len(b), err)
					}
					pinned = append(pinned, held{b, st.want})
				case opOversizedString:
					s, err := c.DoString("GET", "big2")
					if err != nil || s != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: oversized DoString len=%d err=%v", rbuf, chunk, idx, len(s), err)
					}
				case opNestedReply:
					var r Reply
					if err := c.Send("EXEC"); err != nil {
						t.Fatal(err)
					}
					if err := c.Recv(&r); err != nil {
						t.Fatalf("rbuf=%d chunk=%d step=%d: Recv nested %v", rbuf, chunk, idx, err)
					}
					aa, err := r.Array()
					if err != nil || len(aa) != 2 {
						t.Fatal(err)
					}
					sub0, _ := aa[0].Array()
					sub1, _ := aa[1].Array()
					b0, _ := sub0[0].Bytes()
					b1, _ := sub1[1].Bytes()
					if string(b0) != st.wants[0] || string(b1) != st.wants[1] {
						t.Fatalf("rbuf=%d chunk=%d step=%d: nested mismatch %q %q", rbuf, chunk, idx, b0, b1)
					}
					pinned = append(pinned, held{b0, st.wants[0]}, held{b1, st.wants[1]})
				case opPendingErrDo:
					if err := c.Send("BAD"); err != nil {
						t.Fatal(err)
					}
					err := c.DoNoReply("PING")
					re, ok := err.(RedisErr)
					if !ok || re.Error() != st.want {
						t.Fatalf("rbuf=%d chunk=%d step=%d: pending err got %v", rbuf, chunk, idx, err)
					}
					pinned = append(pinned, held{[]byte(re), st.want})
				}
			}
			for j, h := range pinned {
				if string(h.got) != h.want {
					t.Fatalf("rbuf=%d chunk=%d pinned[%d] CORRUPTED: got %q want %q", rbuf, chunk, j, h.got, h.want)
				}
			}
			for _, r := range replies {
				r.Free()
			}
		}
	}
}
