package redisgo

import "errors"

var (
	ErrNil       = errors.New("redisgo: nil")
	ErrMaxActive = errors.New("redisgo: max active connection exceeded")

	errProtocol       = errors.New("redisgo: protocol err")
	errTypeMismatch   = errors.New("redisgo: type mismatch")
	errClosed         = errors.New("redisgo: closed")
	errInvalidArgType = errors.New("redisgo: invalid args type")
)

// RedisErr represents a server side err
// https://redis.io/topics/protocol#resp-errors
type RedisErr []byte

// RedisErr implements error interface
func (err RedisErr) Error() string { return ss(err) }
