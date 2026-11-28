package redisgo

import (
	"context"
	"iter"
	"sync/atomic"
	"time"
)

// PoolConn represents a conn borrowed from a Pool.
// A new PoolConn is handed out by every Pool.Get, so a stale PoolConn
// whose Close was already called cannot interfere with the next borrower.
// A PoolConn must not be copied, as go vet reports: a copy keeps using
// the conn after the original is closed.
type PoolConn struct {
	*Conn

	p      *Pool
	lease  uint64 // Conn.lease when borrowed
	closed atomic.Bool
}

// idleConn is a conn parked in the pool.
type idleConn struct {
	c       *Conn
	freedAt time.Time
}

// CreatedAt returns the create time of the conn, or the zero time after
// Close.
func (c *PoolConn) CreatedAt() time.Time {
	if c.Conn == nil {
		return time.Time{}
	}
	return c.Conn.createdAt
}

// Close puts the conn back to the pool, or closes it if it has replies
// pending or has failed. Calling Close more than once is a no-op.
//
// Close sets the embedded Conn to nil, whose methods return ErrClosed,
// so a PoolConn used after Close fails instead of sending commands on a
// conn that another goroutine may have borrowed since. Code that keeps
// the embedded *Conn itself beyond Close has no such protection.
//
// Only the first Close of a borrow puts the conn back, even through a
// copy of the PoolConn, so the conn is never handed out twice.
func (c *PoolConn) Close() {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	conn := c.Conn
	c.Conn = nil
	if conn != nil && conn.lease.CompareAndSwap(c.lease, c.lease+1) {
		c.p.put(conn)
	}
}

// Pool represents a Conn pool. It is safe for concurrent use.
//
// A conn is closed instead of being put back if it has replies pending, has
// failed, or its session was changed: by SELECT, AUTH, HELLO, RESET, ASKING,
// READONLY, READWRITE, MONITOR, QUIT, SCRIPT DEBUG, a CLIENT subcommand
// other than ID, INFO, LIST, GETNAME, GETREDIR or TRACKINGINFO, a pub/sub
// command, or a MULTI or WATCH not ended by EXEC, DISCARD or UNWATCH. So
// the next caller always gets a fresh session. What DialFunc sets up, such as AUTH
// and SELECT, is the base state of a conn and is kept.
type Pool struct {
	dial DialFunc

	maxIdle   int
	maxActive int64

	maxIdleTime time.Duration
	maxConnTime time.Duration

	active atomic.Int64  // conns owned, idle ones included
	slots  chan struct{} // a token per conn owned, if maxActive > 0
	closed atomic.Bool
	done   chan struct{} // closed by Close to wake up waiting Gets

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
// included. When n conns are owned, Get waits until one is put back or
// closed, the ctx passed to Get is done, or the pool is closed.
// With n <= 0, the default, the number of conns is not limited.
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
	if p.maxIdle > 0 {
		p.ch = make(chan idleConn, p.maxIdle) // else nil: never idle, no handoff
	}
	if p.maxActive > 0 {
		p.slots = make(chan struct{}, p.maxActive)
	}
	p.done = make(chan struct{})
	p.nowfunc = time.Now
	return p
}

// Idle returns idle conn number
func (p *Pool) Idle() int {
	return len(p.ch)
}

// Active returns the number of conns owned by the pool, idle conns included
func (p *Pool) Active() int {
	return int(p.active.Load())
}

// Get returns PoolConn from pool. ctx is passed to DialFunc, and bounds
// the wait for a conn if the pool is limited by WithMaxActive.
//
// The Pool.DoXxx helpers save the boilerplate, and the allocation of the
// PoolConn, for single commands.
func (p *Pool) Get(ctx context.Context) (*PoolConn, error) {
	ic, err := p.get(ctx)
	if err != nil {
		return nil, err
	}
	return &PoolConn{Conn: ic.c, p: p, lease: ic.c.lease.Load()}, nil
}

// Close closes the idle conns and makes waiting and later Get calls fail
// with ErrPoolClosed. Conns in use are closed when they are put back.
// It always returns nil.
func (p *Pool) Close() error {
	if p.closed.CompareAndSwap(false, true) {
		close(p.done)
	}
	p.drain()
	return nil
}

func (p *Pool) drain() {
	for {
		select {
		case ic := <-p.ch:
			p.closeconn(ic.c)
		default:
			return
		}
	}
}

func (p *Pool) get(ctx context.Context) (idleConn, error) {
	if err := ctx.Err(); err != nil {
		return idleConn{}, err
	}
	for {
		if p.closed.Load() {
			return idleConn{}, ErrPoolClosed
		}
		var ic idleConn
		waited := false
		select {
		case ic = <-p.ch:
		default:
			if p.slots == nil {
				return p.dialconn(ctx)
			}
			// take a conn put back, or a slot to dial a new one
			select {
			case ic = <-p.ch:
				waited = true
			case p.slots <- struct{}{}:
				return p.dialconn(ctx)
			case <-ctx.Done():
				return idleConn{}, ctx.Err()
			case <-p.done:
				return idleConn{}, ErrPoolClosed
			}
		}
		if p.closed.Load() || p.expired(ic) {
			p.closeconn(ic.c) // Close may have run since the check above
			if err := ctx.Err(); err != nil {
				return idleConn{}, err // ctx was done while closing it
			}
			continue
		}
		if waited && ctx.Err() != nil {
			p.put(ic.c) // ctx was done while waiting
			return idleConn{}, ctx.Err()
		}
		return ic, nil
	}
}

func (p *Pool) timed() bool {
	return p.maxIdleTime > 0 || p.maxConnTime > 0
}

func (p *Pool) expired(ic idleConn) bool {
	if !p.timed() {
		return false // skip reading the clock
	}
	now := p.nowfunc()
	return (p.maxIdleTime > 0 && now.Sub(ic.freedAt) > p.maxIdleTime) ||
		(p.maxConnTime > 0 && now.Sub(ic.c.createdAt) > p.maxConnTime)
}

// dialconn dials a new conn. The caller holds a slot for it if the pool
// is limited.
func (p *Pool) dialconn(ctx context.Context) (idleConn, error) {
	p.active.Add(1)
	if p.closed.Load() { // Close ran while waiting for the slot
		p.release()
		return idleConn{}, ErrPoolClosed
	}
	c, err := p.dial(ctx)
	if err != nil {
		p.release()
		return idleConn{}, err
	}
	if p.closed.Load() { // Close ran while dialing
		p.closeconn(c)
		return idleConn{}, ErrPoolClosed
	}
	// What DialFunc set up, like SELECT, is the base state. A conn it left
	// in a transaction, watching keys or pushing cannot get back to it once
	// that ends, so it is not reused, and a pushing one stays one, so Do
	// rejects commands on it.
	if c.state&^stateChanged != 0 {
		c.state |= stateChanged
	} else {
		c.state = 0
	}
	c.createdAt = p.nowfunc()
	if err := ctx.Err(); err != nil { // ctx was done while dialing
		p.put(c)
		return idleConn{}, err
	}
	return idleConn{c: c}, nil
}

// release gives up the count and the slot of a conn.
func (p *Pool) release() {
	p.active.Add(-1)
	if p.slots != nil {
		<-p.slots
	}
}

// put parks c in the pool, or closes it if it has replies pending, has
// failed, has a changed session, has lived too long, or the pool is full
// or closed.
func (p *Pool) put(c *Conn) {
	if c.Err() != nil || c.pd != 0 || c.state != 0 || p.closed.Load() {
		p.closeconn(c)
		return
	}
	var now time.Time
	if p.timed() {
		now = p.nowfunc()
		if p.maxConnTime > 0 && now.Sub(c.createdAt) > p.maxConnTime {
			p.closeconn(c)
			return
		}
	}
	select {
	case p.ch <- idleConn{c: c, freedAt: now}:
		if p.closed.Load() {
			p.drain() // Close may have drained the pool before the send
		}
	default:
		p.closeconn(c)
	}
}

func (p *Pool) closeconn(c *Conn) {
	c.Close()
	p.release()
}

// begin gets a conn for a Pool.DoXxx call and bounds its I/O by the
// deadline of ctx. It must be paired with end.
func (p *Pool) begin(ctx context.Context) (idleConn, error) {
	ic, err := p.get(ctx)
	if err != nil {
		return ic, err
	}
	if d, ok := ctx.Deadline(); ok {
		ic.c.setLimit(d)
	}
	return ic, nil
}

// end lifts the deadline of ctx and puts the conn back. A timeout caused by
// the deadline of ctx is reported as context.DeadlineExceeded, even if ctx
// is not marked done yet when the conn times out at the same instant.
func (p *Pool) end(ic idleConn, err error) error {
	if ic.c.br.hasLimit {
		if err != nil && ic.c.timedOutByLimit(err) {
			err = context.DeadlineExceeded
		}
		ic.c.setLimit(time.Time{})
	}
	p.put(ic.c)
	return err
}

// Do gets a conn, calls Conn.Do and puts the conn back.
// The reply stays valid after the conn is reused by others;
// Reply.Free should be called when it is no longer used.
//
// ctx bounds getting the conn as in Get, and its deadline, if earlier than
// the timeouts of the conn, bounds the command. When the deadline passes
// the conn is closed and the error of ctx is returned. Canceling a ctx
// without a deadline does not interrupt a command already sent.
// The other Pool.DoXxx helpers take ctx the same way.
func (p *Pool) Do(ctx context.Context, cmd string, args ...any) (*Reply, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := ic.c.Do(cmd, args...)
	return r, p.end(ic, err)
}

// DoNoReply is Do with Conn.DoNoReply.
func (p *Pool) DoNoReply(ctx context.Context, cmd string, args ...any) error {
	ic, err := p.begin(ctx)
	if err != nil {
		return err
	}
	return p.end(ic, ic.c.DoNoReply(cmd, args...))
}

// DoBytes is Do with Conn.DoBytes.
func (p *Pool) DoBytes(ctx context.Context, cmd string, args ...any) ([]byte, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	b, err := ic.c.DoBytes(cmd, args...)
	return b, p.end(ic, err)
}

// DoString is Do with Conn.DoString.
func (p *Pool) DoString(ctx context.Context, cmd string, args ...any) (string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return "", err
	}
	s, err := ic.c.DoString(cmd, args...)
	return s, p.end(ic, err)
}

// DoBytesSlice is Do with Conn.DoBytesSlice.
func (p *Pool) DoBytesSlice(ctx context.Context, cmd string, args ...any) ([][]byte, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	bb, err := ic.c.DoBytesSlice(cmd, args...)
	return bb, p.end(ic, err)
}

// DoInteger is Do with Conn.DoInteger.
func (p *Pool) DoInteger(ctx context.Context, cmd string, args ...any) (int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	i, err := ic.c.DoInteger(cmd, args...)
	return i, p.end(ic, err)
}

// DoOK is Do with Conn.DoOK.
func (p *Pool) DoOK(ctx context.Context, cmd string, args ...any) (bool, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return false, err
	}
	ok, err := ic.c.DoOK(cmd, args...)
	return ok, p.end(ic, err)
}

// DoInt is Do with Conn.DoInt.
func (p *Pool) DoInt(ctx context.Context, cmd string, args ...any) (int, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	v, err := ic.c.DoInt(cmd, args...)
	return v, p.end(ic, err)
}

// DoInt64 is Do with Conn.DoInt64.
func (p *Pool) DoInt64(ctx context.Context, cmd string, args ...any) (int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	v, err := ic.c.DoInt64(cmd, args...)
	return v, p.end(ic, err)
}

// DoUint64 is Do with Conn.DoUint64.
func (p *Pool) DoUint64(ctx context.Context, cmd string, args ...any) (uint64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	v, err := ic.c.DoUint64(cmd, args...)
	return v, p.end(ic, err)
}

// DoFloat64 is Do with Conn.DoFloat64.
func (p *Pool) DoFloat64(ctx context.Context, cmd string, args ...any) (float64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	v, err := ic.c.DoFloat64(cmd, args...)
	return v, p.end(ic, err)
}

// DoBool is Do with Conn.DoBool.
func (p *Pool) DoBool(ctx context.Context, cmd string, args ...any) (bool, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return false, err
	}
	v, err := ic.c.DoBool(cmd, args...)
	return v, p.end(ic, err)
}

// DoStrings is Do with Conn.DoStrings.
func (p *Pool) DoStrings(ctx context.Context, cmd string, args ...any) ([]string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	v, err := ic.c.DoStrings(cmd, args...)
	return v, p.end(ic, err)
}

// DoInt64s is Do with Conn.DoInt64s.
func (p *Pool) DoInt64s(ctx context.Context, cmd string, args ...any) ([]int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	v, err := ic.c.DoInt64s(cmd, args...)
	return v, p.end(ic, err)
}

// DoFloat64s is Do with Conn.DoFloat64s.
func (p *Pool) DoFloat64s(ctx context.Context, cmd string, args ...any) ([]float64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	v, err := ic.c.DoFloat64s(cmd, args...)
	return v, p.end(ic, err)
}

// DoBools is Do with Conn.DoBools.
func (p *Pool) DoBools(ctx context.Context, cmd string, args ...any) ([]bool, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	v, err := ic.c.DoBools(cmd, args...)
	return v, p.end(ic, err)
}

// DoStringMap is Do with Conn.DoStringMap.
func (p *Pool) DoStringMap(ctx context.Context, cmd string, args ...any) (map[string]string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	v, err := ic.c.DoStringMap(cmd, args...)
	return v, p.end(ic, err)
}

// DoInt64Map is Do with Conn.DoInt64Map.
func (p *Pool) DoInt64Map(ctx context.Context, cmd string, args ...any) (map[string]int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	v, err := ic.c.DoInt64Map(cmd, args...)
	return v, p.end(ic, err)
}

// DoScan is Do with Conn.DoScan.
func (p *Pool) DoScan(ctx context.Context, cmd string, args ...any) (uint64, []string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, nil, err
	}
	cursor, items, err := ic.c.DoScan(cmd, args...)
	return cursor, items, p.end(ic, err)
}

// Scan iterates over keys returned by Redis SCAN, starting from cursor 0.
// Each page borrows a connection via DoScan so no connection is held across
// loop iterations.
func (p *Pool) Scan(ctx context.Context, args ...any) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		callArgs := make([]any, 1+len(args))
		copy(callArgs[1:], args)
		var cursor uint64
		for {
			callArgs[0] = cursor
			next, keys, err := p.DoScan(ctx, "SCAN", callArgs...)
			if err != nil {
				yield("", err)
				return
			}
			for _, k := range keys {
				if !yield(k, nil) {
					return
				}
			}
			if next == 0 {
				return
			}
			cursor = next
		}
	}
}

// WithConn borrows a connection from the pool, passes it to fn, and puts it
// back when fn returns. Unlike Pool.Get, WithConn does not allocate a
// PoolConn wrapper and bounds operations inside fn by ctx's deadline.
func (p *Pool) WithConn(ctx context.Context, fn func(c *Conn) error) error {
	ic, err := p.begin(ctx)
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			ic.c.setLimit(time.Time{})
			p.closeconn(ic.c)
		}
	}()
	err = fn(ic.c)
	completed = true
	return p.end(ic, err)
}

// Pipeline borrows a connection from the pool, runs fn with a Pipeline bound
// to it, drains any unread replies if fn returns nil, and returns the
// connection to the pool.
func (p *Pool) Pipeline(ctx context.Context, fn func(p *Pipeline) error) error {
	ic, err := p.begin(ctx)
	if err != nil {
		return err
	}
	ic.c.pipe = Pipeline{c: ic.c}
	completed := false
	defer func() {
		if !completed {
			ic.c.pipe = Pipeline{}
			ic.c.setLimit(time.Time{})
			p.closeconn(ic.c)
		}
	}()
	err = fn(&ic.c.pipe)
	if err == nil {
		err = ic.c.pipe.Drain()
	}
	ic.c.pipe = Pipeline{}
	completed = true
	return p.end(ic, err)
}
