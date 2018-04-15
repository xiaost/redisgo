package redisgo

import (
	"context"
	"sync/atomic"
	"time"
)

// PoolConn represents a pooled conn
type PoolConn struct {
	*Conn

	freedAt   time.Time
	createdAt time.Time
}

// CreatedAt returns the create time of the conn
func (c *PoolConn) CreatedAt() time.Time {
	return c.createdAt
}

// Pool represents a Conn pool
type Pool struct {
	dial DialFunc

	maxIdle   int
	maxActive int64

	maxIdleTime time.Duration
	maxConnTime time.Duration

	active int64

	ch chan *PoolConn

	nowfunc func() time.Time
}

// PoolOption represents a pool option
type PoolOption func(p *Pool)

// WithMaxIdle limits pool max idle conns to n
func WithMaxIdle(n int) PoolOption {
	return func(p *Pool) {
		p.maxIdle = int(n)
	}
}

// WithMaxActive limits pool outgoing conns to n.
// the Active counter is incr with a success Pool.Get() call,
// and decr after Poo.Put() call.
// ErrMaxActive is returned by Get() if the counter exceeded n
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

type DialFunc func(ctx context.Context) (*Conn, error)

// NewPool creates a instance of Pool with dialfunc
func NewPool(dialfunc DialFunc, ops ...PoolOption) *Pool {
	p := &Pool{dial: dialfunc}
	p.maxIdle = 5
	for _, op := range ops {
		op(p)
	}
	p.ch = make(chan *PoolConn, p.maxIdle)
	p.nowfunc = time.Now
	return p
}

// Idle returns idle conn number
func (p *Pool) Idle() int {
	return len(p.ch)
}

// Active returns active conn number
func (p *Pool) Active() int {
	return int(atomic.LoadInt64(&p.active))
}

// Get returns PoolConn from pool. ctx is passed to DialFunc.
func (p *Pool) Get(ctx context.Context) (*PoolConn, error) {
	select {
	case conn := <-p.ch:
		now := p.nowfunc()
		if (p.maxIdleTime > 0 && now.Sub(conn.freedAt) < p.maxIdleTime) ||
			(p.maxConnTime > 0 && now.Sub(conn.CreatedAt()) < p.maxConnTime) {
			return conn, nil
		}
		p.closeconn(conn)
		return p.Get(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if atomic.AddInt64(&p.active, 1) > p.maxActive {
		atomic.AddInt64(&p.active, -1)
		return nil, ErrMaxActive
	}
	c, err := p.dial(ctx)
	if err != nil {
		atomic.AddInt64(&p.active, -1)
		return nil, err
	}
	return &PoolConn{Conn: c, createdAt: p.nowfunc()}, nil
}

// Put puts conn to the pool.
func (p *Pool) Put(conn *PoolConn) {
	if conn.Err() != nil {
		p.closeconn(conn)
		return
	}
	if p.maxConnTime > 0 && p.nowfunc().Sub(conn.createdAt) > p.maxConnTime {
		p.closeconn(conn)
		return
	}
	conn.freedAt = p.nowfunc()
	select {
	case p.ch <- conn:
		return
	default:
		p.closeconn(conn)
	}
}

func (p *Pool) closeconn(conn *PoolConn) {
	atomic.AddInt64(&p.active, -1)
	conn.Close()
}
