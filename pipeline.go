package redisgo

// Pipeline batches commands on a Conn and reads their replies with sticky
// send/flush error handling and read-buffer reuse.
//
// A Pipeline is not safe for concurrent use and must not outlive the Conn or
// Pool.Pipeline callback it was obtained from.
type Pipeline struct {
	c   *Conn
	err error
	n   int
}

// Send buffers a command on the underlying connection. If an earlier Send or
// Flush on this Pipeline failed, Send is a no-op and preserves that error.
func (p *Pipeline) Send(cmd string, args ...any) {
	if p == nil || p.err != nil {
		return
	}
	if p.c == nil {
		p.err = ErrClosed
		return
	}
	if err := p.c.Send(cmd, args...); err != nil {
		p.err = err
		return
	}
	p.n++
}

// Err returns the sticky Send/Flush error recorded by this Pipeline, or ErrClosed
// if the Pipeline has no underlying connection.
func (p *Pipeline) Err() error {
	if p == nil {
		return ErrClosed
	}
	if p.err != nil {
		return p.err
	}
	if p.c == nil {
		return ErrClosed
	}
	return nil
}

// Len returns the number of commands sent through this Pipeline whose replies
// have not yet been received.
func (p *Pipeline) Len() int {
	if p == nil {
		return 0
	}
	return p.n
}

// Flush writes buffered commands to the underlying connection.
func (p *Pipeline) Flush() error {
	if p == nil || p.c == nil {
		return ErrClosed
	}
	if p.err != nil {
		return p.err
	}
	if err := p.c.Flush(); err != nil {
		p.err = err
		return err
	}
	return nil
}

func (p *Pipeline) prepareRecv() error {
	if p == nil || p.c == nil {
		return ErrClosed
	}
	if p.err != nil {
		return p.err
	}
	if p.n <= 0 {
		return errNoPending
	}
	p.n--
	return nil
}

// Recv receives the next reply into reply.
func (p *Pipeline) Recv(reply *Reply) error {
	if err := p.prepareRecv(); err != nil {
		return err
	}
	return p.c.Recv(reply)
}

// RecvNoReply receives the next reply and returns Reply.Err().
func (p *Pipeline) RecvNoReply() error {
	if err := p.prepareRecv(); err != nil {
		return err
	}
	return p.c.RecvNoReply()
}

// RecvOK receives the next reply and reports whether Redis replied "+OK" (true)
// or nil (false).
func (p *Pipeline) RecvOK() (bool, error) {
	if err := p.prepareRecv(); err != nil {
		return false, err
	}
	return p.c.RecvOK()
}

// RecvBytes receives the next reply and returns Reply.Bytes().
// The returned slice aliases the read buffer of the connection.
func (p *Pipeline) RecvBytes() ([]byte, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvBytes()
}

// RecvString receives the next reply and copies it into a string.
func (p *Pipeline) RecvString() (string, error) {
	if err := p.prepareRecv(); err != nil {
		return "", err
	}
	return p.c.RecvString()
}

// RecvInteger receives the next reply and returns Reply.Integer().
func (p *Pipeline) RecvInteger() (int64, error) {
	if err := p.prepareRecv(); err != nil {
		return 0, err
	}
	return p.c.RecvInteger()
}

// RecvInt receives the next reply and returns Reply.Int().
func (p *Pipeline) RecvInt() (int, error) {
	if err := p.prepareRecv(); err != nil {
		return 0, err
	}
	return p.c.RecvInt()
}

// RecvInt64 receives the next reply and returns Reply.Int64().
func (p *Pipeline) RecvInt64() (int64, error) {
	if err := p.prepareRecv(); err != nil {
		return 0, err
	}
	return p.c.RecvInt64()
}

// RecvUint64 receives the next reply and returns Reply.Uint64().
func (p *Pipeline) RecvUint64() (uint64, error) {
	if err := p.prepareRecv(); err != nil {
		return 0, err
	}
	return p.c.RecvUint64()
}

// RecvFloat64 receives the next reply and returns Reply.Float64().
func (p *Pipeline) RecvFloat64() (float64, error) {
	if err := p.prepareRecv(); err != nil {
		return 0, err
	}
	return p.c.RecvFloat64()
}

// RecvBool receives the next reply and returns Reply.Bool().
func (p *Pipeline) RecvBool() (bool, error) {
	if err := p.prepareRecv(); err != nil {
		return false, err
	}
	return p.c.RecvBool()
}

// RecvBytesSlice receives the next reply and returns Reply.BytesSlice().
// The returned slices alias the read buffer of the connection.
func (p *Pipeline) RecvBytesSlice() ([][]byte, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvBytesSlice()
}

// RecvStrings receives the next reply and returns Reply.Strings().
func (p *Pipeline) RecvStrings() ([]string, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvStrings()
}

// RecvInt64s receives the next reply and returns Reply.Int64s().
func (p *Pipeline) RecvInt64s() ([]int64, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvInt64s()
}

// RecvFloat64s receives the next reply and returns Reply.Float64s().
func (p *Pipeline) RecvFloat64s() ([]float64, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvFloat64s()
}

// RecvBools receives the next reply and returns Reply.Bools().
func (p *Pipeline) RecvBools() ([]bool, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvBools()
}

// RecvStringMap receives the next reply and returns Reply.StringMap().
func (p *Pipeline) RecvStringMap() (map[string]string, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvStringMap()
}

// RecvInt64Map receives the next reply and returns Reply.Int64Map().
func (p *Pipeline) RecvInt64Map() (map[string]int64, error) {
	if err := p.prepareRecv(); err != nil {
		return nil, err
	}
	return p.c.RecvInt64Map()
}

// Drain flushes and receives all unread replies sent through this Pipeline.
// If any Send or Flush failed, Drain still consumes replies for the commands
// already sent so the connection stays in sync, then returns the Send/Flush
// error; otherwise it returns the first RedisErr among the drained replies, or
// any fatal connection error encountered.
func (p *Pipeline) Drain() error {
	if p == nil || p.c == nil {
		return ErrClosed
	}
	if err := p.c.Err(); err != nil {
		if p.err != nil {
			return p.err
		}
		return err
	}
	var pending error
	for p.n > 0 {
		p.n--
		r, err := p.c.recvScratch()
		if err != nil {
			return err
		}
		if pending == nil && r.t == typeError {
			pending = RedisErr(r.b)
			p.c.done(len(r.b) > 0, pending)
		} else {
			p.c.done(false, nil)
		}
	}
	if p.err != nil {
		return p.err
	}
	return pending
}
