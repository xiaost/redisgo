package redisgo

import (
	"context"
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
	_, err = p.Get(ctx)
	if err != ErrMaxActive {
		t.Fatal(err)
	}
	if p.Active() != 2 {
		t.Fatal(p.Active())
	}
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
	if c2.Conn != c0.Conn {
		t.Fatal(c2, c0)
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
	c0.Close()
	now = now.Add(2 * time.Second)
	c1, _ = p.Get(ctx)
	if c0.Conn == c1.Conn {
		t.Fatal(c0, c1)
	}
	c1.Close()
}

func TestPoolConnDoubleClose(t *testing.T) {
	dialfunc := func(ctx context.Context) (*Conn, error) {
		return NewConn(&FakeConn{}), nil
	}
	ctx := context.Background()
	p := NewPool(dialfunc, WithMaxIdle(2))
	c0, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c0.Close()
	c0.Close() // must not panic and must not park the conn twice
	if p.Idle() != 1 || p.Active() != 1 {
		t.Fatal(p.Idle(), p.Active())
	}
	c1, _ := p.Get(ctx)
	if c1.Conn != c0.Conn {
		t.Fatal("expect the idle conn")
	}
	c0.Close() // stale owner, the conn now belongs to c1
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
