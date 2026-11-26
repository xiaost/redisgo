package redisgo

import "errors"

var (
	ErrNil = errors.New("redisgo: nil")
	// ErrPoolClosed is returned by Pool.Get after Pool.Close.
	ErrPoolClosed = errors.New("redisgo: pool closed")
	// ErrClosed is returned by a Conn after Close, and by a PoolConn after
	// its Close.
	ErrClosed = errors.New("redisgo: closed")

	errProtocol       = errors.New("redisgo: protocol err")
	errTypeMismatch   = errors.New("redisgo: type mismatch")
	errInvalidArgType = errors.New("redisgo: invalid args type")
	errPush           = errors.New("redisgo: pub/sub and MONITOR need Send and Recv, not Do")
	errClientReply    = errors.New("redisgo: CLIENT REPLY OFF and SKIP are not supported")
)

// RedisErr represents a server side err
// https://redis.io/topics/protocol#resp-errors
type RedisErr []byte

// RedisErr implements error interface
func (err RedisErr) Error() string { return ss(err) }
