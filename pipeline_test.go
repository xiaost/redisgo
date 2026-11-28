package redisgo

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestConnPipelineRecvAndReuse(t *testing.T) {
	resp := "+OK\r\n" +
		"+OK\r\n" +
		tstr("raw") +
		tstr("str") +
		":10\r\n" +
		":11\r\n" +
		tstr("12") +
		tstr("13") +
		tstr("1.5") +
		":1\r\n" +
		tcmd(tstr("b0")) +
		tcmd(tstr("s0"), tstr("s1")) +
		tcmd(":1\r\n", ":2\r\n") +
		tcmd(tstr("2.5")) +
		tcmd(":0\r\n") +
		tcmd(tstr("f"), tstr("v")) +
		tcmd(tstr("k"), ":3\r\n") +
		tcmd(tstr("alice"), ":30\r\n", tstr("raw")) +
		"+PONG\r\n"

	c := newTestConn(resp, 0)
	pipe := c.Pipeline()
	for range 19 {
		pipe.Send("X")
	}
	if pipe.Len() != 19 || c.Pending() != 19 {
		t.Fatalf("unexpected pending: pipe=%d conn=%d", pipe.Len(), c.Pending())
	}
	if err := pipe.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := pipe.RecvNoReply(); err != nil {
		t.Fatal(err)
	}
	if ok, err := pipe.RecvOK(); err != nil || !ok {
		t.Fatal(ok, err)
	}
	raw, err := pipe.RecvBytes()
	if err != nil || string(raw) != "raw" {
		t.Fatal(raw, err)
	}
	if v, err := pipe.RecvString(); err != nil || v != "str" {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvInteger(); err != nil || v != 10 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvInt(); err != nil || v != 11 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvInt64(); err != nil || v != 12 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvUint64(); err != nil || v != 13 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvFloat64(); err != nil || v != 1.5 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvBool(); err != nil || !v {
		t.Fatal(v, err)
	}
	bb, err := pipe.RecvBytesSlice()
	if err != nil || len(bb) != 1 || string(bb[0]) != "b0" {
		t.Fatal(bb, err)
	}
	if v, err := pipe.RecvStrings(); err != nil || len(v) != 2 || v[1] != "s1" {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvInt64s(); err != nil || len(v) != 2 || v[1] != 2 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvFloat64s(); err != nil || len(v) != 1 || v[0] != 2.5 {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvBools(); err != nil || len(v) != 1 || v[0] {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvStringMap(); err != nil || v["f"] != "v" {
		t.Fatal(v, err)
	}
	if v, err := pipe.RecvInt64Map(); err != nil || v["k"] != 3 {
		t.Fatal(v, err)
	}
	var scanName string
	var scanAge int
	var scanRaw []byte
	if err := pipe.RecvScan(&scanName, &scanAge, &scanRaw); err != nil || scanName != "alice" || scanAge != 30 || string(scanRaw) != "raw" {
		t.Fatal(scanName, scanAge, scanRaw, err)
	}
	var r Reply
	if err := pipe.Recv(&r); err != nil {
		t.Fatal(err)
	}
	if s, err := r.String(); err != nil || s != "PONG" {
		t.Fatal(s, err)
	}
	if _, err := pipe.RecvString(); err != errNoPending {
		t.Fatalf("expect errNoPending, got %v", err)
	}
	if string(raw) != "raw" || string(bb[0]) != "b0" {
		t.Fatal("exposed pipeline bytes corrupted")
	}
	if pipe.Len() != 0 || c.Pending() != 0 || pipe.Err() != nil {
		t.Fatalf("unexpected end state: len=%d pd=%d err=%v", pipe.Len(), c.Pending(), pipe.Err())
	}
}

func TestPoolPipelineAutoDrainAndErrors(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		resp := "+OK\r\n" +
			"-ERR boom\r\n" +
			":1\r\n" +
			"+OK\r\n" +
			"-ERR first\r\n" +
			"+OK\r\n"
		return newTestConn(resp, 0), nil
	}, WithMaxIdle(1), WithMaxActive(1))
	defer p.Close()
	ctx := context.Background()

	// Fire-and-forget returns first RedisErr and keeps conn pooled.
	err := p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("SET", "a", 1)
		pipe.Send("BAD")
		pipe.Send("INCR", "n")
		return nil
	})
	if re, ok := err.(RedisErr); !ok || re.Error() != "ERR boom" {
		t.Fatalf("expect first RedisErr, got %v", err)
	}
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatalf("conn not returned to pool: idle=%d active=%d", p.Idle(), p.Active())
	}

	// Sticky Send error drains only sent commands and returns sticky error.
	err = p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("SET", "x", 1)
		pipe.Send("SET", "y", true) // ErrInvalidArgType
		pipe.Send("SET", "z", 3)    // ignored due to sticky error
		if pipe.Len() != 1 {
			t.Fatalf("expect 1 sent command, got %d", pipe.Len())
		}
		if pipe.Err() != ErrInvalidArgType {
			t.Fatalf("expect ErrInvalidArgType, got %v", pipe.Err())
		}
		return nil
	})
	if err != ErrInvalidArgType {
		t.Fatalf("expect ErrInvalidArgType, got %v", err)
	}
	if p.Idle() != 1 {
		t.Fatal("conn not pooled after sticky arg error")
	}

	// Remaining 2 replies from the connection remain aligned.
	err = p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("BAD2")
		pipe.Send("PING")
		if _, err := pipe.RecvString(); err == nil || err.Error() != "ERR first" {
			t.Fatalf("expect ERR first, got %v", err)
		}
		// Earlier RedisErr must NOT prevent reading remaining replies.
		if s, err := pipe.RecvString(); err != nil || s != "OK" {
			t.Fatalf("expect OK, got %q %v", s, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Idle() != 1 {
		t.Fatal("conn not pooled")
	}
}

func TestPoolPipelineNilAndReadBufferReuse(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte("$-1\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer p.Close()
	ctx := context.Background()

	err := p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("GET", "missing")
		pipe.Send("GET", "missing")
		if _, err := pipe.RecvString(); err != ErrNil {
			t.Fatalf("expect ErrNil, got %v", err)
		}
		if _, err := pipe.RecvInt64(); err != ErrNil {
			t.Fatalf("expect ErrNil, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPoolPipelineNoAllocAndBufferReuse(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte("+OK\r\n:42\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer p.Close()
	ctx := context.Background()

	// Warm up one conn.
	c, _ := p.Get(ctx)
	buf := &c.br.b[0]
	c.Close()

	allocs := testing.AllocsPerRun(200, func() {
		err := p.Pipeline(ctx, func(pipe *Pipeline) error {
			pipe.Send("SET", "k", "v")
			pipe.Send("INCR", "n")
			if err := pipe.RecvNoReply(); err != nil {
				return err
			}
			if n, err := pipe.RecvInt64(); err != nil || n != 42 {
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("expect 0 allocs in Pool.Pipeline, got %v", allocs)
	}
	c, _ = p.Get(ctx)
	if &c.br.b[0] != buf {
		t.Fatal("Pool.Pipeline replaced read buffer instead of reusing it")
	}
	c.Close()
}

func TestPoolPipelinePanicAndTimeout(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		client, server := net.Pipe()
		go io.Copy(io.Discard, server)
		return NewConn(client), nil
	}, WithMaxActive(1), WithMaxIdle(1))
	defer p.Close()
	ctx := context.Background()

	func() {
		defer func() { _ = recover() }()
		_ = p.Pipeline(ctx, func(pipe *Pipeline) error {
			panic("boom")
		})
	}()
	if p.Active() != 0 || p.Idle() != 0 {
		t.Fatalf("panic leaked conn: active=%d idle=%d", p.Active(), p.Idle())
	}

	tctx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	err := p.Pipeline(tctx, func(pipe *Pipeline) error {
		pipe.Send("BLPOP", "k", 0)
		return nil
	})
	if err != context.DeadlineExceeded {
		t.Fatalf("expect DeadlineExceeded, got %v", err)
	}
	if p.Active() != 0 {
		t.Fatal(p.Active())
	}
}

func TestPipelineSwallowedRecvErrorDetectedByDrain(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return newTestConn(strings.Repeat("+OK\r\n", 1), 0), nil
	})
	defer p.Close()
	err := p.Pipeline(context.Background(), func(pipe *Pipeline) error {
		pipe.Send("PING")
		_ = pipe.RecvNoReply() // succeeds
		pipe.Send("PING")
		_ = pipe.RecvNoReply() // hits io.EOF and swallows error
		return nil
	})
	if err != io.EOF {
		t.Fatalf("expect io.EOF surfaced by Drain, got %v", err)
	}
	if p.Idle() != 0 || p.Active() != 0 {
		t.Fatalf("broken conn kept: idle=%d active=%d", p.Idle(), p.Active())
	}
}

func TestPipelineNilAndZeroValue(t *testing.T) {
	var nilPipe *Pipeline
	var x int
	nilPipe.Send("PING")
	if nilPipe.Err() != ErrClosed || nilPipe.Len() != 0 ||
		nilPipe.Flush() != ErrClosed || nilPipe.Drain() != ErrClosed ||
		nilPipe.RecvNoReply() != ErrClosed || nilPipe.RecvScan(&x) != ErrClosed {
		t.Fatal("nil *Pipeline did not behave safely")
	}

	var zeroPipe Pipeline
	if zeroPipe.Err() != ErrClosed || zeroPipe.Len() != 0 ||
		zeroPipe.Flush() != ErrClosed || zeroPipe.Drain() != ErrClosed ||
		zeroPipe.RecvNoReply() != ErrClosed || zeroPipe.RecvScan(&x) != ErrClosed {
		t.Fatal("zero Pipeline before Send did not return ErrClosed")
	}
	zeroPipe.Send("PING")
	if zeroPipe.Err() != ErrClosed || zeroPipe.Len() != 0 {
		t.Fatal(zeroPipe.Err(), zeroPipe.Len())
	}
}

func TestPipelineRecvScan(t *testing.T) {
	// 1. Pool.Pipeline: mixed HMGET, BLPOP timeout (ErrNil), nested RedisErr,
	// non-fatal Scan mismatch, and auto-drain keeping conn aligned & pooled.
	resp := tcmd(tstr("alice"), ":30\r\n", "$-1\r\n", tstr("raw")) +
		"*-1\r\n" +
		tcmd("+OK\r\n", "-ERR inner-pipe\r\n") +
		tcmd(tstr("onlyone")) +
		"+OK\r\n" +
		// Next pipeline call: MULTI/EXEC with RecvScan clears transaction state.
		"+OK\r\n+QUEUED\r\n+QUEUED\r\n" + tcmd(":950\r\n", ":1050\r\n") +
		// WATCH + MULTI + EXEC aborted by conflict (*-1 -> ErrNil) clears stateWatch|stateMulti.
		"+OK\r\n+OK\r\n+QUEUED\r\n*-1\r\n" +
		// Churn replies to prove inner RedisErr stayed pinned.
		strings.Repeat(tstr(strings.Repeat("X", 64)), 64)

	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return newTestConn(resp, 0), nil
	}, WithMaxIdle(1), WithMaxActive(1))
	defer p.Close()
	ctx := context.Background()

	var (
		name     string
		age      int
		miss     = "keep"
		raw      []byte
		innerErr error
	)
	err := p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("HMGET", "user:1", "name", "age", "missing", "raw")
		pipe.Send("BLPOP", "q", 1)
		pipe.Send("EXEC")
		pipe.Send("MGET", "k1")
		pipe.Send("PING") // left unread to test auto-drain

		if err := pipe.RecvScan(&name, &age, &miss, &raw); err != nil {
			return err
		}
		var qk, qv string
		if err := pipe.RecvScan(&qk, &qv); err != ErrNil {
			t.Fatalf("BLPOP timeout RecvScan: expect ErrNil, got %v", err)
		}
		var okStr string
		innerErr = pipe.RecvScan(&okStr, nil)
		if re, ok := innerErr.(RedisErr); !ok || re.Error() != "ERR inner-pipe" || okStr != "OK" {
			t.Fatalf("nested RedisErr RecvScan: got %q %v", okStr, innerErr)
		}
		var a, b string
		if err := pipe.RecvScan(&a, &b); err != ErrTypeMismatch {
			t.Fatalf("short array RecvScan: expect ErrTypeMismatch, got %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "alice" || age != 30 || miss != "" || string(raw) != "raw" {
		t.Fatalf("unexpected Pipeline.RecvScan values: %q %d %q %q", name, age, miss, raw)
	}
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatalf("conn not returned to pool: idle=%d active=%d", p.Idle(), p.Active())
	}

	// MULTI/EXEC via Pipeline.RecvScan clears transaction state and pools conn.
	var balA, balB int64
	err = p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("MULTI")
		pipe.Send("DECRBY", "a", 50)
		pipe.Send("INCRBY", "b", 50)
		pipe.Send("EXEC")
		if err := pipe.RecvNoReply(); err != nil {
			return err
		}
		if err := pipe.RecvNoReply(); err != nil {
			return err
		}
		if err := pipe.RecvNoReply(); err != nil {
			return err
		}
		if err := pipe.RecvScan(&balA, &balB); err != nil {
			return err
		}
		if err := pipe.RecvScan(&balA); err != errNoPending {
			t.Fatalf("expect errNoPending, got %v", err)
		}
		return nil
	})
	if err != nil || balA != 950 || balB != 1050 {
		t.Fatalf("MULTI/EXEC Pipeline.RecvScan: balA=%d balB=%d err=%v", balA, balB, err)
	}
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatalf("conn not pooled after MULTI/EXEC RecvScan: idle=%d active=%d", p.Idle(), p.Active())
	}

	// WATCH + MULTI + EXEC aborted (*-1 -> ErrNil) via Pipeline.RecvScan clears
	// both stateWatch and stateMulti and returns the conn cleanly to the pool.
	err = p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("WATCH", "a")
		pipe.Send("MULTI")
		pipe.Send("INCR", "a")
		pipe.Send("EXEC")
		for range 3 {
			if err := pipe.RecvNoReply(); err != nil {
				return err
			}
		}
		var v int64
		return pipe.RecvScan(&v)
	})
	if err != ErrNil {
		t.Fatalf("aborted WATCH/MULTI/EXEC RecvScan: expect ErrNil, got %v", err)
	}
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatalf("conn not pooled after aborted WATCH/MULTI/EXEC RecvScan: idle=%d active=%d", p.Idle(), p.Active())
	}

	// Churn replies to confirm innerErr and scanned raw bytes were not overwritten.
	err = p.Pipeline(ctx, func(pipe *Pipeline) error {
		for range 64 {
			pipe.Send("GET", "k")
		}
		for range 64 {
			var s string
			if err := pipe.RecvScan(&s); err != nil || len(s) != 64 {
				t.Fatalf("churn Pipeline.RecvScan: len=%d err=%v", len(s), err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if innerErr.Error() != "ERR inner-pipe" || string(raw) != "raw" {
		t.Fatalf("pinned err or copied raw corrupted: %q %q", innerErr, raw)
	}

	// 2. Sticky Send error surfaces on RecvScan without touching connection.
	err = p.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("BAD", struct{}{}) // ErrInvalidArgType
		var v int
		if err := pipe.RecvScan(&v); err != ErrInvalidArgType {
			t.Fatalf("expect sticky ErrInvalidArgType, got %v", err)
		}
		return nil
	})
	if err != ErrInvalidArgType {
		t.Fatalf("expect ErrInvalidArgType from Drain, got %v", err)
	}

	// 3. Swallowed fatal I/O error on RecvScan is detected by Drain and closes conn.
	pEOF := NewPool(func(ctx context.Context) (*Conn, error) {
		return newTestConn("", 0), nil
	})
	defer pEOF.Close()
	err = pEOF.Pipeline(ctx, func(pipe *Pipeline) error {
		pipe.Send("HMGET", "k", "a")
		var v string
		_ = pipe.RecvScan(&v) // swallows io.EOF
		return nil
	})
	if err != io.EOF {
		t.Fatalf("expect io.EOF surfaced by Drain, got %v", err)
	}
	if pEOF.Idle() != 0 || pEOF.Active() != 0 {
		t.Fatalf("broken conn kept: idle=%d active=%d", pEOF.Idle(), pEOF.Active())
	}
}

func TestPoolPipelineRecvScanNoAllocAndBufferReuse(t *testing.T) {
	reply := tcmd(":42\r\n", tstr("1.5"), ":1\r\n", "+OK\r\n", "$1\r\nx\r\n", "$-1\r\n", tstr("token"), "$0\r\n\r\n")
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(reply)}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer p.Close()
	ctx := context.Background()

	c, _ := p.Get(ctx)
	buf := &c.br.b[0]
	c.Close()

	allocs := testing.AllocsPerRun(200, func() {
		var n int64
		var f float64
		var ok bool
		var s1, s2, s3 string
		var stackBuf [16]byte
		raw := stackBuf[:0]
		var empty []byte
		err := p.Pipeline(ctx, func(pipe *Pipeline) error {
			pipe.Send("HMGET", "k", "n", "f", "ok", "s1", "s2", "s3", "raw", "empty")
			return pipe.RecvScan(&n, &f, &ok, &s1, &s2, &s3, &raw, &empty)
		})
		if err != nil {
			t.Fatal(err)
		}
		if n != 42 || f != 1.5 || !ok || s1 != "OK" || s2 != "x" || s3 != "" || string(raw) != "token" || empty == nil || len(empty) != 0 {
			t.Fatalf("unexpected values: %d %v %v %q %q %q %q %#v", n, f, ok, s1, s2, s3, string(raw), empty)
		}
	})
	if allocs != 0 {
		t.Fatalf("expect 0 allocs in Pool.Pipeline RecvScan, got %v", allocs)
	}
	c, _ = p.Get(ctx)
	if &c.br.b[0] != buf {
		t.Fatal("Pool.Pipeline RecvScan replaced read buffer instead of reusing it")
	}
	c.Close()

	// Multiple *string destinations pack into 1 alloc and still reuse read buffer.
	strReply := tcmd(tstr("alice"), tstr("alice@example.com"), tstr("admin"))
	pStr := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte(strReply)}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer pStr.Close()

	c, _ = pStr.Get(ctx)
	buf = &c.br.b[0]
	c.Close()

	allocs = testing.AllocsPerRun(200, func() {
		var u1, u2, u3 string
		err := pStr.Pipeline(ctx, func(pipe *Pipeline) error {
			pipe.Send("HMGET", "user:1", "name", "email", "role")
			return pipe.RecvScan(&u1, &u2, &u3)
		})
		if err != nil {
			t.Fatal(err)
		}
		if u1 != "alice" || u2 != "alice@example.com" || u3 != "admin" {
			t.Fatalf("unexpected strings: %q %q %q", u1, u2, u3)
		}
	})
	if allocs != 1 {
		t.Fatalf("expect 1 alloc in Pool.Pipeline RecvScan(3 *string), got %v", allocs)
	}
	c, _ = pStr.Get(ctx)
	if &c.br.b[0] != buf {
		t.Fatal("Pool.Pipeline RecvScan(strings) replaced read buffer instead of reusing it")
	}
	c.Close()
}
