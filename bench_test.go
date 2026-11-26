package redisgo

import (
	"bufio"
	"context"
	"net"
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
