package redisgo

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolConn(t *testing.T) {
	dialfunc := func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{}), nil
	}
	var now time.Time
	ctx := context.Background()
	p := NewPool(dialfunc,
		WithMaxIdle(1), WithMaxActive(2),
		WithMaxIdleTime(time.Second), WithMaxConnTime(2*time.Second))
	p.nowfunc = func() time.Time {
		return now
	}
	c0, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	_, err = p.Get(tctx)
	cancel()
	if err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if p.Active() != 2 {
		t.Fatal(p.Active())
	}
	conn0 := c0.Conn
	c0.Close()
	if p.Idle() != 1 {
		t.Fatal(p.Idle())
	}
	if p.Active() != 2 {
		t.Fatal(p.Active())
	}
	c1.Close()
	if p.Idle() != 1 {
		t.Fatal(p.Idle())
	}
	if p.Active() != 1 {
		t.Fatal(p.Active())
	}
	c2, _ := p.Get(ctx)
	if c2.Conn != conn0 {
		t.Fatal(c2, conn0)
	}
	now = now.Add(3 * time.Second)
	c2.Close()
	if p.Idle() != 0 {
		t.Fatal(p.Idle())
	}
	if p.Active() != 0 {
		t.Fatal(p.Active())
	}
	c0, _ = p.Get(ctx)
	conn0 = c0.Conn
	c0.Close()
	now = now.Add(2 * time.Second)
	c1, _ = p.Get(ctx)
	if c1.Conn == conn0 {
		t.Fatal(c1, conn0)
	}
	c1.Close()
}

func TestPoolConnDoubleClose(t *testing.T) {
	dialfunc := func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	}
	ctx := context.Background()
	p := NewPool(dialfunc, WithMaxIdle(2))
	c0, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn0 := c0.Conn
	c0.Close()
	c0.Close() // must not panic and must not park the conn twice
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatal(p.Idle(), p.Active())
	}
	c1, _ := p.Get(ctx)
	if c1.Conn != conn0 {
		t.Fatal("expect the idle conn")
	}
	// stale owner, the conn now belongs to c1
	var r Reply
	for name, err := range map[string]error{
		"Send":      c0.Send("SET", "k", "v"),
		"Flush":     c0.Flush(),
		"Recv":      c0.Recv(&r),
		"Err":       c0.Err(),
		"Close":     c0.Conn.Close(),
		"DoNoReply": c0.DoNoReply("SET", "k", "v"),
	} {
		if err != ErrClosed {
			t.Errorf("%s of a closed PoolConn: expect ErrClosed, got %v", name, err)
		}
	}
	if _, err := c0.Do("GET", "k"); err != ErrClosed {
		t.Error(err)
	}
	if _, err := c0.DoString("GET", "k"); err != ErrClosed {
		t.Error(err)
	}
	c0.SetTimeout(time.Second)
	if c0.Conn.Conn() != nil {
		t.Error("closed PoolConn exposes a net.Conn")
	}
	if c1.pd != 0 || c1.DoNoReply("SET", "k", "v") != nil {
		t.Fatal("stale PoolConn interfered with the new owner")
	}
	c0.Close()
	if p.Idle() != 0 {
		t.Fatal("stale Close returned the conn to the pool")
	}
	c2, _ := p.Get(ctx)
	if c2.Conn == c1.Conn {
		t.Fatal("conn handed out to two owners")
	}
	c1.Close()
	c2.Close()
	if p.Idle() != 2 || p.Active() != 2 {
		t.Fatal(p.Idle(), p.Active())
	}
}

func TestPoolGetCanceledCtx(t *testing.T) {
	dialfunc := func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{}), nil
	}
	p := NewPool(dialfunc)
	c, _ := p.Get(context.Background())
	c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Get(ctx); err != context.Canceled {
		t.Fatal(err)
	}
	if p.Idle() != 1 {
		t.Fatal(p.Idle())
	}
}

func TestPoolZeroMaxIdle(t *testing.T) {
	dialfunc := func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{}), nil
	}
	p := NewPool(dialfunc, WithMaxIdle(-1))
	c, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if p.Idle() != 0 || p.Active() != 0 {
		t.Fatal(p.Idle(), p.Active())
	}
}

func TestPoolDo(t *testing.T) {
	const resp = "+OK\r\n$5\r\nhello\r\n$5\r\nworld\r\n:42\r\n*2\r\n$1\r\na\r\n$-1\r\n-ERR boom\r\n"
	dials := 0
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		dials++
		return newTestConn(resp, 3), nil
	})
	ctx := context.Background()
	if err := p.DoNoReply(ctx, "SET", "k", "hello"); err != nil {
		t.Fatal(err)
	}
	r, err := p.Do(ctx, "GET", "k")
	if err != nil {
		t.Fatal(err)
	}
	if s, err := p.DoString(ctx, "GET", "k"); err != nil || s != "world" {
		t.Fatal(s, err)
	}
	if n, err := p.DoInteger(ctx, "INCR", "n"); err != nil || n != 42 {
		t.Fatal(n, err)
	}
	if bb, err := p.DoBytesSlice(ctx, "MGET", "a", "b"); err != nil || len(bb) != 2 || string(bb[0]) != "a" || bb[1] != nil {
		t.Fatal(bb, err)
	}
	if _, err := p.DoBytes(ctx, "GET", "k"); err == nil || err.Error() != "ERR boom" {
		t.Fatal(err)
	}
	// the conn has been reused since Do returned, its reply must be intact
	if b, err := r.Bytes(); err != nil || string(b) != "hello" {
		t.Fatalf("reply of Pool.Do corrupted: %q %v", b, err)
	}
	r.Free()
	if dials != 1 || p.Idle() != 1 || p.Active() != 1 {
		t.Fatal(dials, p.Idle(), p.Active())
	}
	// the stream is exhausted, the failed conn must not go back to the pool
	if err := p.DoNoReply(ctx, "PING"); err != io.EOF {
		t.Fatal(err)
	}
	if p.Idle() != 0 || p.Active() != 0 {
		t.Fatal(p.Idle(), p.Active())
	}
}

func TestPoolDoNoAlloc(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	dctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	for _, ctx := range []context.Context{context.Background(), dctx} {
		allocs := testing.AllocsPerRun(1000, func() {
			if err := p.DoNoReply(ctx, "SET", "k", "v"); err != nil {
				t.Fatal(err)
			}
		})
		if allocs != 0 {
			t.Fatalf("expect 0 allocs, got %v", allocs)
		}
	}
}

func TestPoolClose(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{}), nil
	})
	ctx := context.Background()
	c0, _ := p.Get(ctx)
	c1, _ := p.Get(ctx)
	conn0, conn1 := c0.Conn, c1.Conn
	c0.Close()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if p.Idle() != 0 || p.Active() != 1 || conn0.Err() != ErrClosed {
		t.Fatal(p.Idle(), p.Active(), conn0.Err())
	}
	c1.Close() // in use during Close, closed when put back
	if p.Idle() != 0 || p.Active() != 0 || conn1.Err() != ErrClosed {
		t.Fatal(p.Idle(), p.Active(), conn1.Err())
	}
	if _, err := p.Get(ctx); err != ErrPoolClosed {
		t.Fatal(err)
	}
	if err := p.DoNoReply(ctx, "PING"); err != ErrPoolClosed {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestPoolCloseConcurrent checks that no conn is left open when Close races
// with conns being put back.
func TestPoolCloseConcurrent(t *testing.T) {
	for range 20 {
		p := NewPool(func(ctx context.Context) (*Conn, error) {
			return NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
		}, WithMaxIdle(4))
		ctx := context.Background()
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				for range 100 {
					if err := p.DoNoReply(ctx, "SET", "k", "v"); err == ErrPoolClosed {
						return
					} else if err != nil {
						t.Error(err)
						return
					}
				}
			})
		}
		p.Close()
		wg.Wait()
		if p.Idle() != 0 || p.Active() != 0 {
			t.Fatal(p.Idle(), p.Active())
		}
	}
}

// TestPoolConcurrentAliasing checks that bytes returned by DoBytes stay
// intact while other goroutines reuse the conn, under the race detector.
func TestPoolConcurrentAliasing(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{reply: []byte("$5\r\nhello\r\n")},
			WithReadBuffer(0), WithReadTimeout(0), WithWriteTimeout(0)), nil
	}, WithMaxIdle(2))
	defer p.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			var kept [][]byte
			for i := range 300 {
				if i%2 == 0 {
					b, err := p.DoBytes(ctx, "GET", "k")
					if err != nil {
						t.Error(err)
						return
					}
					kept = append(kept, b)
				} else if s, err := p.DoString(ctx, "GET", "k"); err != nil || s != "hello" {
					t.Error(s, err)
					return
				}
			}
			for _, b := range kept {
				if string(b) != "hello" {
					t.Errorf("corrupted: %q", b)
					return
				}
			}
		})
	}
	wg.Wait()
}

// waitGet starts a Get and checks that it blocks.
func waitGet(t *testing.T, p *Pool, ctx context.Context) <-chan error {
	t.Helper()
	res := make(chan error, 1)
	go func() {
		c, err := p.Get(ctx)
		if err == nil {
			defer c.Close()
		}
		res <- err
	}()
	select {
	case err := <-res:
		t.Fatalf("expect Get to wait, got %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	return res
}

func TestPoolWaitsAtMaxActive(t *testing.T) {
	var dials atomic.Int32
	newPool := func(maxIdle int) *Pool {
		dials.Store(0)
		return NewPool(func(ctx context.Context) (*Conn, error) {
			dials.Add(1)
			return NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
		}, WithMaxIdle(maxIdle), WithMaxActive(1))
	}
	ctx := context.Background()

	for _, maxIdle := range []int{1, 0} {
		// woken up by a conn put back, or by its slot if it cannot idle
		p := newPool(maxIdle)
		c0, _ := p.Get(ctx)
		res := waitGet(t, p, ctx)
		c0.Close()
		if err := <-res; err != nil {
			t.Fatal(maxIdle, err)
		}
		if p.Active() > 1 {
			t.Fatal(maxIdle, p.Active())
		}
	}

	// woken up by the slot of a failed conn
	p := newPool(1)
	c0, _ := p.Get(ctx)
	res := waitGet(t, p, ctx)
	c0.Conn.Close()
	c0.Close()
	if err := <-res; err != nil || dials.Load() != 2 {
		t.Fatal(err, dials.Load())
	}

	// woken up by Close
	p = newPool(1)
	c0, _ = p.Get(ctx)
	res = waitGet(t, p, ctx)
	p.Close()
	if err := <-res; err != ErrPoolClosed {
		t.Fatal(err)
	}
	c0.Close()
	if p.Active() != 0 {
		t.Fatal(p.Active())
	}

	// woken up by ctx
	p = newPool(1)
	c0, _ = p.Get(ctx)
	cctx, cancel := context.WithCancel(ctx)
	res = waitGet(t, p, cctx)
	cancel()
	if err := <-res; err != context.Canceled {
		t.Fatal(err)
	}
	c0.Close()
}

func TestPoolMaxActiveConcurrent(t *testing.T) {
	const maxActive = 3
	var p *Pool
	p = NewPool(func(ctx context.Context) (*Conn, error) {
		if n := p.Active(); n > maxActive {
			t.Errorf("dialing with %d active conns", n)
		}
		return NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0)), nil
	}, WithMaxIdle(1), WithMaxActive(maxActive))
	defer p.Close()
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 200 {
				if err := p.DoNoReply(ctx, "SET", "k", "v"); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if p.Active() > maxActive || p.Idle() > 1 {
		t.Fatal(p.Active(), p.Idle())
	}
}

// recordConn records the deadlines set on it.
type recordConn struct {
	FakeConn
	rdl, wdl time.Time
	rn, wn   int
}

func (c *recordConn) SetReadDeadline(t time.Time) error  { c.rdl = t; c.rn++; return nil }
func (c *recordConn) SetWriteDeadline(t time.Time) error { c.wdl = t; c.wn++; return nil }

func TestPoolDoCtxDeadline(t *testing.T) {
	rc := &recordConn{FakeConn: FakeConn{reply: []byte("+OK\r\n")}}
	conn := NewConn(rc, WithReadTimeout(0), WithWriteTimeout(0))
	p := NewPool(func(ctx context.Context) (*Conn, error) { return conn, nil })
	bg := context.Background()

	d := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(bg, d)
	defer cancel()
	if err := p.DoNoReply(ctx, "SET", "k", "v"); err != nil {
		t.Fatal(err)
	}
	if !rc.rdl.Equal(d) || !rc.wdl.Equal(d) {
		t.Fatalf("expect the deadline of ctx, got %v %v", rc.rdl, rc.wdl)
	}
	// without timeouts, the deadline of the last call must be lifted
	if err := p.DoNoReply(bg, "SET", "k", "v"); err != nil {
		t.Fatal(err)
	}
	if !rc.rdl.IsZero() || !rc.wdl.IsZero() {
		t.Fatalf("expect no deadline, got %v %v", rc.rdl, rc.wdl)
	}
	n := rc.rn + rc.wn
	if err := p.DoNoReply(bg, "SET", "k", "v"); err != nil || rc.rn+rc.wn != n {
		t.Fatal("deadlines updated without timeouts or ctx", err)
	}

	// the timeouts of the conn win when they are earlier
	conn.SetTimeout(time.Minute)
	if err := p.DoNoReply(ctx, "SET", "k", "v"); err != nil {
		t.Fatal(err)
	}
	if !rc.rdl.Before(d) || rc.rdl.After(time.Now().Add(time.Minute)) {
		t.Fatalf("expect the read timeout to win, got %v", rc.rdl)
	}
}

func TestPoolDoCtxTimeout(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		client, server := net.Pipe()
		go io.Copy(io.Discard, server) // read commands, never reply
		// keep the default timeouts of 30s, far beyond the ctx
		return NewConn(client), nil
	})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := p.DoNoReply(ctx, "BLPOP", "k", 0); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("ctx deadline ignored, took %v", d)
	}
	if p.Active() != 0 || p.Idle() != 0 {
		t.Fatal("timed out conn not closed", p.Active(), p.Idle())
	}
}

// TestPoolDoConnTimeoutWins checks that a timeout of the conn, earlier than
// the deadline of ctx, is not reported as the error of ctx.
func TestPoolDoConnTimeoutWins(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		client, server := net.Pipe()
		go io.Copy(io.Discard, server) // read commands, never reply
		return NewConn(client, WithReadTimeout(20*time.Millisecond)), nil
	})
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	err := p.DoNoReply(ctx, "BLPOP", "k", 0)
	if !errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

// deadlineErrConn fails to set deadlines.
type deadlineErrConn struct{ FakeConn }

var errNoDeadline = errors.New("deadline not supported")

func (c *deadlineErrConn) SetReadDeadline(time.Time) error  { return errNoDeadline }
func (c *deadlineErrConn) SetWriteDeadline(time.Time) error { return errNoDeadline }

func TestDeadlineErrorIsFatal(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []Option
	}{
		{"write", []Option{WithReadTimeout(0)}},
		{"read", []Option{WithWriteTimeout(0)}},
	} {
		c := NewConn(&deadlineErrConn{FakeConn{reply: []byte("+OK\r\n")}}, tc.opts...)
		if err := c.DoNoReply("PING"); err != errNoDeadline || c.Err() != errNoDeadline {
			t.Errorf("%s: expect the deadline error, got %v %v", tc.name, err, c.Err())
		}
	}
	// without timeouts no deadline is needed
	c := NewConn(&deadlineErrConn{FakeConn{reply: []byte("+OK\r\n")}}, WithReadTimeout(0), WithWriteTimeout(0))
	if err := c.DoNoReply("PING"); err != nil {
		t.Fatal(err)
	}
}

// writeCountConn counts writes.
type writeCountConn struct {
	FakeConn
	writes int
}

func (c *writeCountConn) Write(b []byte) (int, error) { c.writes++; return len(b), nil }

func TestPoolCtxDoneWhileDialing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	wc := &writeCountConn{FakeConn: FakeConn{reply: []byte("+OK\r\n")}}
	p := NewPool(func(context.Context) (*Conn, error) {
		cancel() // the caller gives up while the conn is being set up
		return NewConn(wc, WithReadTimeout(0), WithWriteTimeout(0)), nil
	})
	defer p.Close()
	if err := p.DoNoReply(ctx, "SET", "k", "v"); err != context.Canceled {
		t.Fatal(err)
	}
	if wc.writes != 0 {
		t.Fatal("command sent for a canceled ctx")
	}
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatal("healthy conn not kept", p.Idle(), p.Active())
	}
}

func TestPoolDialAfterClose(t *testing.T) {
	dials := 0
	p := NewPool(func(context.Context) (*Conn, error) {
		dials++
		return NewConn(&FakeConn{}), nil
	}, WithMaxActive(1))
	p.Close()
	p.slots <- struct{}{} // a Get that won the slot as Close ran
	if _, err := p.dialconn(context.Background()); err != ErrPoolClosed {
		t.Fatal(err)
	}
	if dials != 0 || p.Active() != 0 || len(p.slots) != 0 {
		t.Fatal(dials, p.Active(), len(p.slots))
	}
}

// closeHookConn runs onClose when closed.
type closeHookConn struct {
	writeCountConn
	onClose func()
}

func (c *closeHookConn) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return nil
}

func TestPoolCtxDoneWhileClosingExpired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var conns []*closeHookConn
	p := NewPool(func(context.Context) (*Conn, error) {
		hc := &closeHookConn{writeCountConn: writeCountConn{FakeConn: FakeConn{reply: []byte("+OK\r\n")}}}
		conns = append(conns, hc)
		return NewConn(hc, WithReadTimeout(0), WithWriteTimeout(0)), nil
	}, WithMaxIdle(2), WithMaxIdleTime(time.Second))
	defer p.Close()
	var now time.Time
	p.nowfunc = func() time.Time { return now }
	a, _ := p.Get(ctx)
	b, _ := p.Get(ctx)
	conns[0].onClose = cancel // the caller gives up while A is closed
	a.Close()                 // idle since 0, expired at the next Get
	now = now.Add(2 * time.Second)
	b.Close() // idle since 2s, still fresh
	now = now.Add(500 * time.Millisecond)
	if err := p.DoNoReply(ctx, "SET", "k", "v"); err != context.Canceled {
		t.Fatal(err)
	}
	if conns[1].writes != 0 || p.Idle() != 1 {
		t.Fatal("command sent for a canceled ctx", conns[1].writes, p.Idle())
	}
}

// TestPoolConnCopyClose checks that a copy of a PoolConn cannot put the
// conn back a second time.
func TestPoolConnCopyClose(t *testing.T) {
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{}), nil
	}, WithMaxIdle(2), WithMaxActive(1))
	ctx := context.Background()
	pc, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// what dup := *pc does, without copying the atomic flag
	dup := &PoolConn{Conn: pc.Conn, p: pc.p, lease: pc.lease}
	pc.Close()
	dup.Close()
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatal("conn put back twice", p.Idle(), p.Active())
	}
	done := make(chan struct{})
	go func() {
		p.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Pool.Close blocked")
	}
	if p.Active() != 0 {
		t.Fatal(p.Active())
	}
}

func TestPoolCloseWhileDialing(t *testing.T) {
	var p *Pool
	p = NewPool(func(ctx context.Context) (*Conn, error) {
		p.Close()
		return NewConn(&FakeConn{}), nil
	}, WithMaxActive(1))
	if _, err := p.Get(context.Background()); err != ErrPoolClosed {
		t.Fatal(err)
	}
	if p.Active() != 0 {
		t.Fatal(p.Active())
	}
}

// TestPoolZeroMaxIdleWaiter checks that with MaxIdle 0 a waiting Get dials
// instead of getting the conn put back.
func TestPoolZeroMaxIdleWaiter(t *testing.T) {
	var dials atomic.Int32
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		dials.Add(1)
		return NewConn(&FakeConn{}), nil
	}, WithMaxIdle(0), WithMaxActive(1))
	ctx := context.Background()
	c0, _ := p.Get(ctx)
	res := waitGet(t, p, ctx)
	c0.Close()
	if err := <-res; err != nil || dials.Load() != 2 {
		t.Fatal(err, dials.Load())
	}
}
