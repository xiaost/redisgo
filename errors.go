package redisgo

import "errors"

var (
	// ErrNil is returned when redis replies with a nil bulk string or nil array.
	ErrNil = errors.New("redisgo: nil")
	// ErrPoolClosed is returned by Pool.Get after Pool.Close.
	ErrPoolClosed = errors.New("redisgo: pool closed")
	// ErrClosed is returned by a Conn after Close, and by a PoolConn after
	// its Close.
	ErrClosed = errors.New("redisgo: closed")
	// ErrProtocol is returned when a malformed RESP reply is received.
	ErrProtocol = errors.New("redisgo: protocol err")
	// ErrTypeMismatch is returned when a reply has an unexpected RESP type.
	ErrTypeMismatch = errors.New("redisgo: type mismatch")
	// ErrInvalidArgType is returned when an unsupported command argument or Scan
	// destination type is provided.
	ErrInvalidArgType = errors.New("redisgo: invalid args type")

	errPush        = errors.New("redisgo: pub/sub and MONITOR need Send and Recv, not Do")
	errClientReply = errors.New("redisgo: CLIENT REPLY OFF and SKIP are not supported")
	errNoPending   = errors.New("redisgo: no pending pipeline reply")
)

// RedisErr represents a server side err
// https://redis.io/topics/protocol#resp-errors
type RedisErr []byte

// RedisErr implements error interface
func (err RedisErr) Error() string { return ss(err) }

// HasPrefix reports whether the error message starts with prefix,
// such as "NOSCRIPT", "WRONGTYPE", "MOVED" or "ASK".
func (err RedisErr) HasPrefix(prefix string) bool {
	return len(err) >= len(prefix) && ss(err[:len(prefix)]) == prefix
}
