package redisgo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
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
