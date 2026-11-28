package redisgo

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
)

// Script represents a Lua script cached on Redis by its SHA1 digest.
// It executes via EVALSHA and transparently falls back to EVAL if Redis
// responds with a NOSCRIPT error.
type Script struct {
	keyCount int
	src      string
	hash     string
}

// NewScript creates a Script for Lua source src taking keyCount keys.
// If keyCount < 0, the first element of keysAndArgs passed to Do or Send
// methods is sent as the dynamic number of keys.
func NewScript(keyCount int, src string) *Script {
	sum := sha1.Sum([]byte(src))
	return &Script{
		keyCount: keyCount,
		src:      src,
		hash:     hex.EncodeToString(sum[:]),
	}
}

// Hash returns the lowercase hex SHA1 digest of the script.
func (s *Script) Hash() string {
	return s.hash
}

// Src returns the Lua source code of the script.
func (s *Script) Src() string {
	return s.src
}

func isNoScript(err error) bool {
	re, ok := err.(RedisErr)
	return ok && (re.HasPrefix("NOSCRIPT ") || (len(re) == len("NOSCRIPT") && ss(re) == "NOSCRIPT"))
}

func (s *Script) callConn(c *Conn, cmd, scriptArg string, keysAndArgs []any) (*Reply, error) {
	if s.keyCount >= 0 {
		return c.Do(cmd, scriptArg, s.keyCount, keysAndArgs)
	}
	return c.Do(cmd, scriptArg, keysAndArgs)
}

func (s *Script) callScratch(c *Conn, cmd, scriptArg string, keysAndArgs []any) (*Reply, error) {
	if s.keyCount >= 0 {
		return c.do(cmd, []any{scriptArg, s.keyCount, keysAndArgs})
	}
	return c.do(cmd, []any{scriptArg, keysAndArgs})
}

func (s *Script) do(c *Conn, keysAndArgs []any) (*Reply, error) {
	r, err := s.callScratch(c, "EVALSHA", s.hash, keysAndArgs)
	if err != nil {
		return nil, err
	}
	if isNoScript(r.Err()) {
		c.done(false, nil)
		return s.callScratch(c, "EVAL", s.src, keysAndArgs)
	}
	return r, nil
}

// SendHash sends EVALSHA for the script without waiting for a reply.
func (s *Script) SendHash(c *Conn, keysAndArgs ...any) error {
	if s.keyCount >= 0 {
		return c.Send("EVALSHA", s.hash, s.keyCount, keysAndArgs)
	}
	return c.Send("EVALSHA", s.hash, keysAndArgs)
}

// Send sends EVAL for the script without waiting for a reply.
func (s *Script) Send(c *Conn, keysAndArgs ...any) error {
	if s.keyCount >= 0 {
		return c.Send("EVAL", s.src, s.keyCount, keysAndArgs)
	}
	return c.Send("EVAL", s.src, keysAndArgs)
}

// Load executes SCRIPT LOAD on p so subsequent EVALSHA calls succeed.
func (s *Script) Load(ctx context.Context, p *Pool) error {
	return p.DoNoReply(ctx, "SCRIPT", "LOAD", s.src)
}

// DoConn executes the script on c using EVALSHA, falling back to EVAL on
// NOSCRIPT. Reply.Free must be called when the returned Reply is no longer used.
func (s *Script) DoConn(c *Conn, keysAndArgs ...any) (*Reply, error) {
	if c == nil {
		return nil, ErrClosed
	}
	r, err := s.callConn(c, "EVALSHA", s.hash, keysAndArgs)
	if err != nil {
		return nil, err
	}
	if isNoScript(r.Err()) {
		r.Free()
		return s.callConn(c, "EVAL", s.src, keysAndArgs)
	}
	return r, nil
}

// Do executes the script on a pooled connection, falling back from EVALSHA to
// EVAL on NOSCRIPT. Reply.Free must be called when the returned Reply is no
// longer used.
func (s *Script) Do(ctx context.Context, p *Pool, keysAndArgs ...any) (*Reply, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.DoConn(ic.c, keysAndArgs...)
	return r, p.end(ic, err)
}

// DoNoReply executes the script on a pooled connection and returns Reply.Err().
func (s *Script) DoNoReply(ctx context.Context, p *Pool, keysAndArgs ...any) error {
	ic, err := p.begin(ctx)
	if err != nil {
		return err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return p.end(ic, err)
	}
	err = r.Err()
	ic.c.done(false, err)
	return p.end(ic, err)
}

// DoOK executes the script on a pooled connection and reports whether Redis
// replied "+OK" (true) or nil (false).
func (s *Script) DoOK(ctx context.Context, p *Pool, keysAndArgs ...any) (bool, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return false, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return false, p.end(ic, err)
	}
	ok, err := r.ok()
	ic.c.done(false, err)
	return ok, p.end(ic, err)
}

// DoBytes executes the script on a pooled connection and returns Reply.Bytes().
// The returned slice aliases the read buffer of the connection.
func (s *Script) DoBytes(ctx context.Context, p *Pool, keysAndArgs ...any) ([]byte, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	b, err := r.Bytes()
	ic.c.done(true, err)
	return b, p.end(ic, err)
}

// DoString executes the script on a pooled connection and returns a string copy.
func (s *Script) DoString(ctx context.Context, p *Pool, keysAndArgs ...any) (string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return "", err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return "", p.end(ic, err)
	}
	v, err := r.String()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoInt executes the script on a pooled connection and returns Reply.Int().
func (s *Script) DoInt(ctx context.Context, p *Pool, keysAndArgs ...any) (int, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return 0, p.end(ic, err)
	}
	v, err := r.Int()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoInt64 executes the script on a pooled connection and returns Reply.Int64().
func (s *Script) DoInt64(ctx context.Context, p *Pool, keysAndArgs ...any) (int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return 0, p.end(ic, err)
	}
	v, err := r.Int64()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoUint64 executes the script on a pooled connection and returns Reply.Uint64().
func (s *Script) DoUint64(ctx context.Context, p *Pool, keysAndArgs ...any) (uint64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return 0, p.end(ic, err)
	}
	v, err := r.Uint64()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoFloat64 executes the script on a pooled connection and returns Reply.Float64().
func (s *Script) DoFloat64(ctx context.Context, p *Pool, keysAndArgs ...any) (float64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return 0, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return 0, p.end(ic, err)
	}
	v, err := r.Float64()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoBool executes the script on a pooled connection and returns Reply.Bool().
func (s *Script) DoBool(ctx context.Context, p *Pool, keysAndArgs ...any) (bool, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return false, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return false, p.end(ic, err)
	}
	v, err := r.Bool()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoBytesSlice executes the script on a pooled connection and returns
// Reply.BytesSlice(). The returned slices alias the read buffer of the connection.
func (s *Script) DoBytesSlice(ctx context.Context, p *Pool, keysAndArgs ...any) ([][]byte, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.BytesSlice()
	ic.c.done(true, err)
	return v, p.end(ic, err)
}

// DoStrings executes the script on a pooled connection and returns Reply.Strings().
func (s *Script) DoStrings(ctx context.Context, p *Pool, keysAndArgs ...any) ([]string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.Strings()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoInt64s executes the script on a pooled connection and returns Reply.Int64s().
func (s *Script) DoInt64s(ctx context.Context, p *Pool, keysAndArgs ...any) ([]int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.Int64s()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoFloat64s executes the script on a pooled connection and returns Reply.Float64s().
func (s *Script) DoFloat64s(ctx context.Context, p *Pool, keysAndArgs ...any) ([]float64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.Float64s()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoBools executes the script on a pooled connection and returns Reply.Bools().
func (s *Script) DoBools(ctx context.Context, p *Pool, keysAndArgs ...any) ([]bool, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.Bools()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoStringMap executes the script on a pooled connection and returns Reply.StringMap().
func (s *Script) DoStringMap(ctx context.Context, p *Pool, keysAndArgs ...any) (map[string]string, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.StringMap()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}

// DoInt64Map executes the script on a pooled connection and returns Reply.Int64Map().
func (s *Script) DoInt64Map(ctx context.Context, p *Pool, keysAndArgs ...any) (map[string]int64, error) {
	ic, err := p.begin(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.do(ic.c, keysAndArgs)
	if err != nil {
		return nil, p.end(ic, err)
	}
	v, err := r.Int64Map()
	ic.c.done(false, err)
	return v, p.end(ic, err)
}
