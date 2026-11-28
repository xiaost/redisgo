package redisgo

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func benchConn(reply string) *Conn {
	return NewConn(&FakeConn{reply: []byte(reply)}, WithReadTimeout(0), WithWriteTimeout(0))
}

func BenchmarkDoNoReply(b *testing.B) {
	b.ReportAllocs()
	c := benchConn("+OK\r\n")
	for b.Loop() {
		if err := c.DoNoReply("SET", "key", "value"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoInteger(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(":12345\r\n")
	for b.Loop() {
		if _, err := c.DoInteger("INCR", "key"); err != nil {
			b.Fatal(err)
		}
	}
}

var value100 = strings.Repeat("v", 100)

func BenchmarkDoBytes(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(tstr(value100))
	for b.Loop() {
		if _, err := c.DoBytes("GET", "key"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoString(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(tstr(value100))
	for b.Loop() {
		if _, err := c.DoString("GET", "key"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoFree(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(tstr(value100))
	for b.Loop() {
		r, err := c.Do("GET", "key")
		if err != nil {
			b.Fatal(err)
		}
		r.Free()
	}
}

func mgetReply(n int) string {
	aa := make([]string, n)
	for i := range aa {
		aa[i] = tstr("value")
	}
	return tcmd(aa...)
}

func BenchmarkDoBytesSlice(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(mgetReply(10))
	for b.Loop() {
		if _, err := c.DoBytesSlice("MGET", "k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecvArray100(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(mgetReply(100))
	var r Reply
	for b.Loop() {
		if err := c.Send("MGET", "k"); err != nil {
			b.Fatal(err)
		}
		if err := c.Recv(&r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPoolGetDoClose(b *testing.B) {
	b.ReportAllocs()
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return benchConn("+OK\r\n"), nil
	})
	ctx := context.Background()
	for b.Loop() {
		c, err := p.Get(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if err := c.DoNoReply("SET", "key", "value"); err != nil {
			b.Fatal(err)
		}
		c.Close()
	}
}

func BenchmarkPoolDoNoReply(b *testing.B) {
	b.ReportAllocs()
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return benchConn("+OK\r\n"), nil
	})
	ctx := context.Background()
	for b.Loop() {
		if err := p.DoNoReply(ctx, "SET", "key", "value"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoOK(b *testing.B) {
	b.ReportAllocs()
	c := benchConn("+OK\r\n")
	for b.Loop() {
		if _, err := c.DoOK("SET", "key", "value", "NX"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoInt64(b *testing.B) {
	b.ReportAllocs()
	c := benchConn("$5\r\n12345\r\n")
	for b.Loop() {
		if _, err := c.DoInt64("GET", "counter"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoFloat64(b *testing.B) {
	b.ReportAllocs()
	c := benchConn("$6\r\n123.45\r\n")
	for b.Loop() {
		if _, err := c.DoFloat64("ZSCORE", "z", "m"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoBool(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(":1\r\n")
	for b.Loop() {
		if _, err := c.DoBool("EXISTS", "key"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoStrings(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(mgetReply(10))
	for b.Loop() {
		if _, err := c.DoStrings("MGET", "k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7", "k8", "k9"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoStringMap(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(mgetReply(20))
	for b.Loop() {
		if _, err := c.DoStringMap("HGETALL", "hash"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecvInt64(b *testing.B) {
	b.ReportAllocs()
	c := benchConn("$5\r\n12345\r\n")
	for b.Loop() {
		if err := c.Send("GET", "counter"); err != nil {
			b.Fatal(err)
		}
		if _, err := c.RecvInt64(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPoolWithConn(b *testing.B) {
	b.ReportAllocs()
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return benchConn("+OK\r\n"), nil
	})
	ctx := context.Background()
	for b.Loop() {
		err := p.WithConn(ctx, func(c *Conn) error {
			return c.DoNoReply("SET", "key", "value")
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPoolPipeline(b *testing.B) {
	b.ReportAllocs()
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return benchConn("+OK\r\n:1\r\n$3\r\n100\r\n"), nil
	})
	ctx := context.Background()
	for b.Loop() {
		err := p.Pipeline(ctx, func(pipe *Pipeline) error {
			pipe.Send("SET", "k", "v")
			pipe.Send("INCR", "n")
			pipe.Send("GET", "n")
			if err := pipe.RecvNoReply(); err != nil {
				return err
			}
			if _, err := pipe.RecvInt64(); err != nil {
				return err
			}
			if _, err := pipe.RecvInt64(); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScriptDoInt64(b *testing.B) {
	b.ReportAllocs()
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return benchConn(":42\r\n"), nil
	})
	sc := NewScript(1, "return redis.call('INCR', KEYS[1])")
	ctx := context.Background()
	for b.Loop() {
		if _, err := sc.DoInt64(ctx, p, "counter"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoStringsLarge(b *testing.B) {
	b.ReportAllocs()
	keys := make([]string, 200)
	for i := range keys {
		keys[i] = tstr("session:01234567-89ab-cdef-0123-456789ab" + strconv.Itoa(1000+i))
	}
	c := benchConn(tcmd(keys...))
	for b.Loop() {
		if _, err := c.DoStrings("SMEMBERS", "sessions"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDoStringMapLarge(b *testing.B) {
	b.ReportAllocs()
	pairs := make([]string, 200)
	for i := range 100 {
		pairs[2*i] = tstr("field_name_long_" + strconv.Itoa(1000+i))
		pairs[2*i+1] = tstr("field_value_payload_data_long_" + strconv.Itoa(1000+i))
	}
	c := benchConn(tcmd(pairs...))
	for b.Loop() {
		if _, err := c.DoStringMap("HGETALL", "hash"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSendLarge128KB(b *testing.B) {
	b.ReportAllocs()
	val := strings.Repeat("b", 128<<10)
	c := benchConn("+OK\r\n")
	for b.Loop() {
		if err := c.DoNoReply("SET", "bigkey", val); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPoolScan(b *testing.B) {
	b.ReportAllocs()
	keys := make([]string, 30)
	for i := range keys {
		keys[i] = tstr("session:key:" + strconv.Itoa(1000+i))
	}
	scanResp := tcmd(tstr("1001"), tcmd(keys[:10]...)) +
		tcmd(tstr("2002"), "*0\r\n") +
		tcmd(tstr("3003"), tcmd(keys[10:20]...)) +
		tcmd(tstr("4004"), "*0\r\n") +
		tcmd(tstr("0"), tcmd(keys[20:30]...))
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return benchConn(scanResp), nil
	})
	ctx := context.Background()
	for b.Loop() {
		for _, err := range p.Scan(ctx, "MATCH", "session:*", "COUNT", 10) {
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkReplyScanStrings(b *testing.B) {
	b.ReportAllocs()
	c := benchConn(tcmd(tstr("alice"), tstr("alice@example.com"), tstr("admin"), tstr("us-west"), tstr("active")))
	var r Reply
	if err := c.Recv(&r); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		var s1, s2, s3, s4, s5 string
		if err := r.Scan(&s1, &s2, &s3, &s4, &s5); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNewConn(b *testing.B) {
	b.ReportAllocs()
	fc := &FakeConn{}
	for b.Loop() {
		_ = NewConn(fc)
	}
}

// serveOK runs a fake server on loopback that answers every command with
// +OK, so benchmarks pay for real deadlines and syscalls.
func serveOK(b *testing.B) net.Conn {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Skip(err)
	}
	b.Cleanup(func() { ln.Close() })
	go func() {
		s, err := ln.Accept()
		if err != nil {
			return
		}
		defer s.Close()
		r := bufio.NewReader(s)
		w := bufio.NewWriter(s)
		for {
			line, err := r.ReadSlice('\n')
			if err != nil {
				return
			}
			if line[0] == '*' { // one reply per command
				w.WriteString("+OK\r\n")
			}
			if r.Buffered() == 0 {
				if w.Flush() != nil {
					return
				}
			}
		}
	}()
	nc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { nc.Close() })
	return nc
}

// BenchmarkPipelineTCP sends 64 commands and then receives 64 replies over a
// loopback socket with timeouts enabled.
func BenchmarkPipelineTCP(b *testing.B) {
	b.ReportAllocs()
	c := NewConn(serveOK(b), WithReadTimeout(time.Second), WithWriteTimeout(time.Second))
	var r Reply
	const depth = 64
	for b.Loop() {
		for range depth {
			if err := c.Send("SET", "key", "value"); err != nil {
				b.Fatal(err)
			}
		}
		for range depth {
			if err := c.Recv(&r); err != nil {
				b.Fatal(err)
			}
		}
	}
}
