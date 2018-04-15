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
	c0.Free()
	if p.Idle() != 1 {
		t.Fatal(p.Idle())
	}
	if p.Active() != 2 {
		t.Fatal(p.Active())
	}
	c1.Free()
	if p.Idle() != 1 {
		t.Fatal(p.Idle())
	}
	if p.Active() != 1 {
		t.Fatal(p.Active())
	}
	c2, _ := p.Get(ctx)
	if c2 != c0 {
		t.Fatal(c2, c0)
	}
	now = now.Add(3 * time.Second)
	c2.Free()
	if p.Idle() != 0 {
		t.Fatal(p.Idle())
	}
	if p.Active() != 0 {
		t.Fatal(p.Active())
	}
	c0, _ = p.Get(ctx)
	c0.Free()
	now = now.Add(2 * time.Second)
	c1, _ = p.Get(ctx)
	if c0 == c1 {
		t.Fatal(c0, c1)
	}
	c1.Free()
}
