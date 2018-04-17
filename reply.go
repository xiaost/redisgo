package redisgo

import (
	"sync"
)

type replyType int

const (
	typeUnset replyType = iota
	typeNil
	typeError
	typeInteger
	typeSString
	typeBString
	typeNilArray
	typeArray
)

// Reply represents a reply of redis
type Reply struct {
	p     bool
	t     replyType
	b     []byte
	i     int64
	err   RedisErr
	array []Reply
}

var replyPool = sync.Pool{
	New: func() interface{} {
		r := new(Reply)
		r.p = true
		return r
	},
}

// Reset resets fields of Reply
func (r *Reply) Reset() {
	r.t = typeUnset
	r.b = nil // never reuse it
	r.i = -1
	for i := range r.array {
		r.array[i].Reset() // remove ref for gc friendly
	}
	r.array = r.array[:0]
}

// IsNil returns true if redis response a "Null Bulk String"
func (r *Reply) IsNil() bool {
	return r.t == typeNil
}

// IsOK returns true if redis reply "+OK"
func (r *Reply) IsOK() bool {
	return r.t == typeSString && len(r.b) == 2 && r.b[0] == 'O' && r.b[1] == 'K'
}

// Bytes returns bytes of "Simple Strings" and "Bulk Strings" protocol:
// https://redis.io/topics/protocol#resp-simple-strings and
// https://redis.io/topics/protocol#resp-bulk-strings
func (r *Reply) Bytes() ([]byte, error) {
	if err := r.Err(); err != nil {
		return nil, err
	}
	if r.t != typeSString && r.t != typeBString {
		return nil, errTypeMismatch
	}
	return r.b, nil
}

// Integer returns int64 of integer protocol:
// https://redis.io/topics/protocol#resp-integers
func (r *Reply) Integer() (int64, error) {
	if err := r.Err(); err != nil {
		return 0, err
	}
	if r.t != typeInteger {
		return 0, errTypeMismatch
	}
	return r.i, nil
}

// Array returns []Reply of array protocol:
// https://redis.io/topics/protocol#resp-arrays
func (r *Reply) Array() ([]Reply, error) {
	if err := r.Err(); err != nil {
		return nil, err
	}
	if r.t != typeArray && r.t != typeNilArray {
		return nil, errTypeMismatch
	}
	if r.t == typeNilArray {
		return nil, nil
	}
	return r.array, nil
}

// Err returns ErrNil or RedisErr or nil
func (r *Reply) Err() error {
	if r.t == typeError {
		return r.err
	}
	if r.t == typeNil {
		return ErrNil
	}
	return nil
}

// Free resets Reply and put it back to memory pool
func (r *Reply) Free() {
	if r == nil {
		return
	}
	r.Reset()
	if r.p {
		replyPool.Put(r)
	}
}
