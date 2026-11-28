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
		"+PONG\r\n"

	c := newTestConn(resp, 0)
	pipe := c.Pipeline()
	for range 18 {
		pipe.Send("X")
	}
	if pipe.Len() != 18 || c.Pending() != 18 {
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
	nilPipe.Send("PING")
	if nilPipe.Err() != ErrClosed || nilPipe.Len() != 0 ||
		nilPipe.Flush() != ErrClosed || nilPipe.Drain() != ErrClosed ||
		nilPipe.RecvNoReply() != ErrClosed {
		t.Fatal("nil *Pipeline did not behave safely")
	}

	var zeroPipe Pipeline
	if zeroPipe.Err() != ErrClosed || zeroPipe.Len() != 0 ||
		zeroPipe.Flush() != ErrClosed || zeroPipe.Drain() != ErrClosed ||
		zeroPipe.RecvNoReply() != ErrClosed {
		t.Fatal("zero Pipeline before Send did not return ErrClosed")
	}
	zeroPipe.Send("PING")
	if zeroPipe.Err() != ErrClosed || zeroPipe.Len() != 0 {
		t.Fatal(zeroPipe.Err(), zeroPipe.Len())
	}
}
