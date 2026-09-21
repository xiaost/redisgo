package redisgo

import (
	"context"
	"sync/atomic"
	"time"
)

// PoolConn represents a conn borrowed from a Pool.
// A new PoolConn is handed out by every Pool.Get, so a stale PoolConn
// whose Close was already called cannot interfere with the next borrower.
type PoolConn struct {
	*Conn

	p         *Pool
	createdAt time.Time
	closed    atomic.Bool
}

// idleConn is a conn parked in the pool.
type idleConn struct {
	c         *Conn
	createdAt time.Time
	freedAt   time.Time
}

// CreatedAt returns the create time of the conn
func (c *PoolConn) CreatedAt() time.Time {
	return c.createdAt
}

// Close puts the conn back to the pool, or closes it if it has replies
// pending or has failed. Calling Close more than once is a no-op.
func (c *PoolConn) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	if c.Conn.pd != 0 {
		c.Conn.Close()
	}
	c.p.put(c.Conn, c.createdAt)
}

// Pool represents a Conn pool
type Pool struct {
	dial DialFunc

	maxIdle   int
	maxActive int64

	maxIdleTime time.Duration
	maxConnTime time.Duration

	active int64

	ch chan idleConn

	nowfunc func() time.Time
}

// PoolOption represents a pool option
type PoolOption func(p *Pool)

// WithMaxIdle limits pool max idle conns to n, default: 5.
// With n <= 0 conns are never kept idle and every Get dials.
func WithMaxIdle(n int) PoolOption {
	return func(p *Pool) {
		p.maxIdle = max(n, 0)
	}
}

// WithMaxActive limits the conns owned by the pool to n, idle conns
// included. The counter is incremented when Get dials a new conn and
// decremented when a conn is closed. Get returns ErrMaxActive instead of
// dialing when the counter would exceed n.
func WithMaxActive(n int) PoolOption {
	return func(p *Pool) {
		p.maxActive = int64(n)
	}
}

// WithMaxIdleTime sets max idle time of a connection in pool
func WithMaxIdleTime(t time.Duration) PoolOption {
	return func(p *Pool) {
		p.maxIdleTime = t
	}
}

// WithMaxConnTime sets max conn time of a connection in pool from dial opertion
func WithMaxConnTime(t time.Duration) PoolOption {
	return func(p *Pool) {
		p.maxConnTime = t
	}
}

// DialFunc creates a Conn. It is called by Pool.Get with the ctx passed to Get.
type DialFunc func(ctx context.Context) (*Conn, error)

// NewPool creates a instance of Pool with dialfunc
func NewPool(dialfunc DialFunc, ops ...PoolOption) *Pool {
	p := &Pool{dial: dialfunc}
	p.maxIdle = 5
	for _, op := range ops {
		op(p)
	}
	p.ch = make(chan idleConn, p.maxIdle)
	p.nowfunc = time.Now
	return p
}

// Idle returns idle conn number
func (p *Pool) Idle() int {
	return len(p.ch)
}

// Active returns the number of conns owned by the pool, idle conns included
func (p *Pool) Active() int {
	return int(atomic.LoadInt64(&p.active))
}

// Get returns PoolConn from pool. ctx is passed to DialFunc.
func (p *Pool) Get(ctx context.Context) (*PoolConn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for {
		var ic idleConn
		select {
		case ic = <-p.ch:
		default:
			return p.dialconn(ctx)
		}
		now := p.nowfunc()
		if (p.maxIdleTime > 0 && now.Sub(ic.freedAt) > p.maxIdleTime) ||
			(p.maxConnTime > 0 && now.Sub(ic.createdAt) > p.maxConnTime) {
			p.closeconn(ic.c)
			continue
		}
		return &PoolConn{Conn: ic.c, p: p, createdAt: ic.createdAt}, nil
	}
}

func (p *Pool) dialconn(ctx context.Context) (*PoolConn, error) {
	active := atomic.AddInt64(&p.active, 1)
	if p.maxActive > 0 && active > p.maxActive {
		atomic.AddInt64(&p.active, -1)
		return nil, ErrMaxActive
	}
	c, err := p.dial(ctx)
	if err != nil {
		atomic.AddInt64(&p.active, -1)
		return nil, err
	}
	return &PoolConn{Conn: c, p: p, createdAt: p.nowfunc()}, nil
}

func (p *Pool) put(c *Conn, createdAt time.Time) {
	if c.Err() != nil {
		p.closeconn(c)
		return
	}
	now := p.nowfunc()
	if p.maxConnTime > 0 && now.Sub(createdAt) > p.maxConnTime {
		p.closeconn(c)
		return
	}
	select {
	case p.ch <- idleConn{c: c, createdAt: createdAt, freedAt: now}:
	default:
		p.closeconn(c)
	}
}

func (p *Pool) closeconn(c *Conn) {
	atomic.AddInt64(&p.active, -1)
	c.Close()
}
