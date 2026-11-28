package redisgo

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"net"
	"testing"
	"time"
)

func TestScriptHashAndExec(t *testing.T) {
	const src = "return redis.call('INCR', KEYS[1])"
	sum := sha1.Sum([]byte(src))
	wantHash := hex.EncodeToString(sum[:])

	sc := NewScript(1, src)
	if sc.Hash() != wantHash || sc.Src() != src {
		t.Fatalf("hash=%q src=%q", sc.Hash(), sc.Src())
	}

	// EVALSHA returns NOSCRIPT, then EVAL succeeds.
	conn := &scriptedConn{chunkConn: chunkConn{data: []byte("-NOSCRIPT No matching script.\r\n:7\r\n")}}
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0)), nil
	}, WithMaxIdle(1))
	defer p.Close()

	ctx := context.Background()
	n, err := sc.DoInt64(ctx, p, "counter", "arg1")
	if err != nil || n != 7 {
		t.Fatalf("expect 7, got %d %v", n, err)
	}
	wantSent := tcmd(tstr("EVALSHA"), tstr(wantHash), tstr("1"), tstr("counter"), tstr("arg1")) +
		tcmd(tstr("EVAL"), tstr(src), tstr("1"), tstr("counter"), tstr("arg1"))
	if conn.written.String() != wantSent {
		t.Fatalf("unexpected commands sent:\nwant: %q\ngot:  %q", wantSent, conn.written.String())
	}
	if p.Idle() != 1 {
		t.Fatal("conn not returned to pool")
	}
}

func TestScriptDynamicKeyCountAndDoConn(t *testing.T) {
	sc := NewScript(-1, "return {KEYS[1], ARGV[1]}")
	conn := &scriptedConn{chunkConn: chunkConn{data: []byte("-NOSCRIPT missing\r\n" + tcmd(tstr("k1"), tstr("a1")))}}
	c := NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0))

	r, err := sc.DoConn(c, 1, "k1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Free()
	ss, err := r.Strings()
	if err != nil || len(ss) != 2 || ss[0] != "k1" || ss[1] != "a1" {
		t.Fatal(ss, err)
	}
	wantSent := tcmd(tstr("EVALSHA"), tstr(sc.Hash()), tstr("1"), tstr("k1"), tstr("a1")) +
		tcmd(tstr("EVAL"), tstr(sc.Src()), tstr("1"), tstr("k1"), tstr("a1"))
	if conn.written.String() != wantSent {
		t.Fatalf("unexpected dynamic keyCount encoding:\nwant: %q\ngot:  %q", wantSent, conn.written.String())
	}

	if _, err := sc.DoConn(nil, 1, "k1"); err != ErrClosed {
		t.Fatal(err)
	}
}

func TestScriptNonNoScriptErrorDoesNotRetry(t *testing.T) {
	sc := NewScript(1, "return 1")
	for _, errMsg := range []string{"-ERR user error\r\n", "-NOSCRIPT_CUSTOM nope\r\n"} {
		conn := &scriptedConn{chunkConn: chunkConn{data: []byte(errMsg + ":1\r\n")}}
		p := NewPool(func(ctx context.Context) (*Conn, error) {
			return NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0)), nil
		})
		_, err := sc.DoString(context.Background(), p, "k")
		if err == nil || err.Error() != errMsg[1:len(errMsg)-2] {
			t.Fatal(err)
		}
		if got := conn.written.String(); got != tcmd(tstr("EVALSHA"), tstr(sc.Hash()), tstr("1"), tstr("k")) {
			t.Fatalf("unexpected retry on %q: %q", errMsg, got)
		}
		// Error message must survive connection reuse.
		if s, okErr := p.DoInt64(context.Background(), "PING"); okErr != nil || s != 1 {
			t.Fatal(s, okErr)
		}
		if err.Error() != errMsg[1:len(errMsg)-2] {
			t.Fatalf("RedisErr corrupted after reuse: %q", err.Error())
		}
		p.Close()
	}
}

func TestScriptPoolDoFallbackAndClosedPool(t *testing.T) {
	sc := NewScript(1, "return redis.call('GET', KEYS[1])")
	conn := &scriptedConn{chunkConn: chunkConn{data: []byte("-NOSCRIPT missing\r\n$3\r\nval\r\n")}}
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	ctx := context.Background()
	r, err := sc.Do(ctx, p, "k")
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.String()
	r.Free()
	if err != nil || s != "val" {
		t.Fatal(s, err)
	}
	wantSent := tcmd(tstr("EVALSHA"), tstr(sc.Hash()), tstr("1"), tstr("k")) +
		tcmd(tstr("EVAL"), tstr(sc.Src()), tstr("1"), tstr("k"))
	if conn.written.String() != wantSent {
		t.Fatalf("unexpected commands:\nwant: %q\ngot:  %q", wantSent, conn.written.String())
	}
	p.Close()
	if _, err := sc.DoInt64(ctx, p, "k"); err != ErrPoolClosed {
		t.Fatalf("expect ErrPoolClosed, got %v", err)
	}
}

func TestScriptAllHelpersAndLoadSend(t *testing.T) {
	sc := NewScript(1, "return 1")
	resp := "+OK\r\n" +
		"+OK\r\n" +
		"+OK\r\n" +
		tstr("raw") +
		tstr("str") +
		":5\r\n" +
		":6\r\n" +
		tstr("7") +
		tstr("2.5") +
		":1\r\n" +
		tcmd(tstr("b")) +
		tcmd(tstr("s")) +
		tcmd(":1\r\n") +
		tcmd(tstr("1.5")) +
		tcmd(":1\r\n") +
		tcmd(tstr("k"), tstr("v")) +
		tcmd(tstr("k"), ":9\r\n") +
		"+OK\r\n"

	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return newTestConn(resp, 0), nil
	})
	defer p.Close()
	ctx := context.Background()

	if err := sc.Load(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := sc.DoNoReply(ctx, p, "k"); err != nil {
		t.Fatal(err)
	}
	if ok, err := sc.DoOK(ctx, p, "k"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if b, err := sc.DoBytes(ctx, p, "k"); err != nil || string(b) != "raw" {
		t.Fatal(b, err)
	}
	if s, err := sc.DoString(ctx, p, "k"); err != nil || s != "str" {
		t.Fatal(s, err)
	}
	if v, err := sc.DoInt(ctx, p, "k"); err != nil || v != 5 {
		t.Fatal(v, err)
	}
	if v, err := sc.DoInt64(ctx, p, "k"); err != nil || v != 6 {
		t.Fatal(v, err)
	}
	if v, err := sc.DoUint64(ctx, p, "k"); err != nil || v != 7 {
		t.Fatal(v, err)
	}
	if v, err := sc.DoFloat64(ctx, p, "k"); err != nil || v != 2.5 {
		t.Fatal(v, err)
	}
	if v, err := sc.DoBool(ctx, p, "k"); err != nil || !v {
		t.Fatal(v, err)
	}
	if v, err := sc.DoBytesSlice(ctx, p, "k"); err != nil || len(v) != 1 || string(v[0]) != "b" {
		t.Fatal(v, err)
	}
	if v, err := sc.DoStrings(ctx, p, "k"); err != nil || len(v) != 1 || v[0] != "s" {
		t.Fatal(v, err)
	}
	if v, err := sc.DoInt64s(ctx, p, "k"); err != nil || len(v) != 1 || v[0] != 1 {
		t.Fatal(v, err)
	}
	if v, err := sc.DoFloat64s(ctx, p, "k"); err != nil || len(v) != 1 || v[0] != 1.5 {
		t.Fatal(v, err)
	}
	if v, err := sc.DoBools(ctx, p, "k"); err != nil || len(v) != 1 || !v[0] {
		t.Fatal(v, err)
	}
	if v, err := sc.DoStringMap(ctx, p, "k"); err != nil || v["k"] != "v" {
		t.Fatal(v, err)
	}
	if v, err := sc.DoInt64Map(ctx, p, "k"); err != nil || v["k"] != 9 {
		t.Fatal(v, err)
	}
	r, err := sc.Do(ctx, p, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsOK() {
		t.Fatal("expected OK")
	}
	r.Free()

	// SendHash and Send on Conn.
	scConn := &scriptedConn{chunkConn: chunkConn{data: []byte(":1\r\n:2\r\n")}}
	c := NewConn(scConn, WithReadTimeout(0), WithWriteTimeout(0))
	if err := sc.SendHash(c, "k", 1); err != nil {
		t.Fatal(err)
	}
	if err := sc.Send(c, "k", 2); err != nil {
		t.Fatal(err)
	}
	if v, err := c.RecvInt64(); err != nil || v != 1 {
		t.Fatal(v, err)
	}
	if v, err := c.RecvInt64(); err != nil || v != 2 {
		t.Fatal(v, err)
	}
}

func TestScriptNoAlloc(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(":42\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer p.Close()
	sc := NewScript(1, "return redis.call('INCR', KEYS[1])")
	ctx := context.Background()
	allocs := testing.AllocsPerRun(200, func() {
		v, err := sc.DoInt64(ctx, p, "counter")
		if err != nil || v != 42 {
			t.Fatal(v, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("expect 0 allocs on EVALSHA hit, got %v", allocs)
	}
}

func TestScriptDeadline(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		client, server := net.Pipe()
		go func() {
			buf := make([]byte, 256)
			for {
				if _, err := server.Read(buf); err != nil {
					return
				}
			}
		}()
		return NewConn(client), nil
	})
	defer p.Close()
	sc := NewScript(1, "return 1")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := sc.DoInt64(ctx, p, "k"); err != context.DeadlineExceeded {
		t.Fatalf("expect DeadlineExceeded, got %v", err)
	}
}

func TestScriptBufferReuseOnNoScriptAndNil(t *testing.T) {
	// DoConn fallback on NOSCRIPT followed by an integer reply should not pin c.br.
	sc := NewScript(1, "return 42")
	conn := &scriptedConn{chunkConn: chunkConn{data: []byte("-NOSCRIPT missing\r\n:42\r\n")}}
	c := NewConn(conn, WithReadTimeout(0), WithWriteTimeout(0))
	r, err := sc.DoConn(c, "k")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := r.Int64(); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	r.Free()
	if c.br.pin != 0 {
		t.Fatalf("DoConn NOSCRIPT fallback to integer reply pinned read buffer: pin=%d", c.br.pin)
	}

	// Script.DoBytes on nil reply should not pin read buffer.
	cNil := NewConn(&FakeConn{reply: []byte("$-1\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	buf := &cNil.br.b[0]
	p := NewPool(func(ctx context.Context) (*Conn, error) { return cNil, nil })
	defer p.Close()
	for range 1000 {
		if b, err := sc.DoBytes(context.Background(), p, "k"); err != ErrNil || b != nil {
			t.Fatal(b, err)
		}
	}
	if &cNil.br.b[0] != buf || cNil.br.pin != 0 {
		t.Fatalf("Script.DoBytes(ErrNil) pinned read buffer: reused=%v pin=%d", &cNil.br.b[0] == buf, cNil.br.pin)
	}
}

func TestScriptNestedArgDepth(t *testing.T) {
	// Nested []any at maxArgDepth-1 (7) levels must succeed identically to
	// Conn.Do("EVALSHA", ...) in Script.DoInt64, DoConn, SendHash, and Send,
	// for both keyCount >= 0 and keyCount < 0.
	deep := []any{"k1", "v1"}
	for range maxArgDepth - 1 {
		deep = []any{deep}
	}

	scFixed := NewScript(1, "return 1")
	pFixed := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(":1\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer pFixed.Close()
	if n, err := scFixed.DoInt64(context.Background(), pFixed, deep); err != nil || n != 1 {
		t.Fatalf("Script(1).DoInt64(depth %d): n=%d err=%v", maxArgDepth-1, n, err)
	}
	cConn := NewConn(&FakeConn{reply: []byte(":2\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	r, err := scFixed.DoConn(cConn, deep)
	if err != nil {
		t.Fatalf("Script(1).DoConn(depth %d): %v", maxArgDepth-1, err)
	}
	r.Free()

	cSend := NewConn(&FakeConn{reply: []byte(":3\r\n:4\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	if err := scFixed.SendHash(cSend, deep); err != nil {
		t.Fatalf("Script(1).SendHash(depth %d): %v", maxArgDepth-1, err)
	}
	if err := scFixed.Send(cSend, deep); err != nil {
		t.Fatalf("Script(1).Send(depth %d): %v", maxArgDepth-1, err)
	}
	if n, err := cSend.RecvInt64(); err != nil || n != 3 {
		t.Fatalf("SendHash RecvInt64: %d %v", n, err)
	}
	if n, err := cSend.RecvInt64(); err != nil || n != 4 {
		t.Fatalf("Send RecvInt64: %d %v", n, err)
	}

	scDyn := NewScript(-1, "return 1")
	pDyn := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(":5\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer pDyn.Close()
	if n, err := scDyn.DoInt64(context.Background(), pDyn, 1, deep); err != nil || n != 5 {
		t.Fatalf("Script(-1).DoInt64(depth %d): n=%d err=%v", maxArgDepth-1, n, err)
	}
	r, err = scDyn.DoConn(NewConn(&FakeConn{reply: []byte(":6\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), 1, deep)
	if err != nil {
		t.Fatalf("Script(-1).DoConn(depth %d): %v", maxArgDepth-1, err)
	}
	r.Free()

	cDynSend := NewConn(&FakeConn{reply: []byte(":7\r\n:8\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	if err := scDyn.SendHash(cDynSend, 1, deep); err != nil {
		t.Fatalf("Script(-1).SendHash(depth %d): %v", maxArgDepth-1, err)
	}
	if err := scDyn.Send(cDynSend, 1, deep); err != nil {
		t.Fatalf("Script(-1).Send(depth %d): %v", maxArgDepth-1, err)
	}
	if n, err := cDynSend.RecvInt64(); err != nil || n != 7 {
		t.Fatalf("Script(-1) SendHash RecvInt64: %d %v", n, err)
	}
	if n, err := cDynSend.RecvInt64(); err != nil || n != 8 {
		t.Fatalf("Script(-1) Send RecvInt64: %d %v", n, err)
	}

	// Stack-backed []byte passed to Script.DoInt64 must remain 0 allocs.
	pStack := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(":42\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer pStack.Close()
	ctx := context.Background()
	allocs := testing.AllocsPerRun(200, func() {
		var key [8]byte
		copy(key[:], "counter")
		n, err := scFixed.DoInt64(ctx, pStack, key[:7])
		if err != nil || n != 42 {
			t.Fatal(n, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("Script.DoInt64(stack []byte): expect 0 allocs, got %v", allocs)
	}
}
