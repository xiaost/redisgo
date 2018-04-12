package redisgo

import "errors"

var (
	ErrNil = errors.New("redisgo: nil")

	errProtocol       = errors.New("redisgo: protocol err")
	errTypeMismatch   = errors.New("redisgo: type mismatch")
	errClosed         = errors.New("redisgo: closed")
	errInvalidArgType = errors.New("redisgo: invalid args type")
)

// RedisErr represents a server side err
// https://redis.io/topics/protocol#resp-errors
type RedisErr string

// RedisErr implements error interface
func (err RedisErr) Error() string { return "redis: " + string(err) }
