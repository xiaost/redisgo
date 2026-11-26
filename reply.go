package redisgo

import (
	"sync"
)

type replyType uint8

const (
	typeUnset replyType = iota
	typeNil
	typeError
	typeInteger
	typeSString
	typeBString
	typeArray
)

// Reply represents a reply of redis
type Reply struct {
	t     replyType
	p     bool   // from replyPool
	hide  uint32 // array[:hide] may keep nested arrays, see unhide
	b     []byte // text of simple strings, bulk strings and errors
	i     int64
	array []Reply
}

var replyPool = sync.Pool{
	New: func() any {
		r := new(Reply)
		r.p = true
		return r
	},
}

// NewReply creates Reply instance from pool
func NewReply() *Reply {
	return replyPool.Get().(*Reply)
}

// maxNested bounds the elements kept by the arrays nested in a reply for
// reuse, all together, so that many arrays each within maxArrayPrealloc
// do not pin a huge reply either.
const maxNested = 4 * maxArrayPrealloc

// Reset resets fields of Reply. It keeps the array for reuse, and the
// arrays nested in it, except the ones larger than maxArrayPrealloc, or
// all of them if together they keep more than maxNested elements.
func (r *Reply) Reset() {
	r.reset(false)
}

// queued reports whether r is the QUEUED status of a command in a
// transaction.
func (r *Reply) queued() bool {
	return r.t == typeSString && string(r.b) == "QUEUED"
}

// IsNil returns true if redis replied a "Null Bulk String" or a "Null Array",
// such as GET of a missing key, BLPOP on timeout or EXEC of an aborted
// transaction. The getters return ErrNil for both.
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
//
// The returned slice aliases the read buffer of the Conn, see Conn.
func (r *Reply) Bytes() ([]byte, error) {
	if r.t != typeSString && r.t != typeBString {
		return nil, r.mismatch()
	}
	return r.b, nil
}

// Integer returns int64 of integer protocol:
// https://redis.io/topics/protocol#resp-integers
func (r *Reply) Integer() (int64, error) {
	if r.t != typeInteger {
		return 0, r.mismatch()
	}
	return r.i, nil
}

// Array returns []Reply of array protocol:
// https://redis.io/topics/protocol#resp-arrays
func (r *Reply) Array() ([]Reply, error) {
	if r.t != typeArray {
		return nil, r.mismatch()
	}
	return r.array, nil
}

// Err returns ErrNil or RedisErr or nil
func (r *Reply) Err() error {
	switch r.t {
	case typeError:
		return RedisErr(r.b)
	case typeNil:
		return ErrNil
	}
	return nil
}

// mismatch returns the error of a getter called on a reply of another type.
func (r *Reply) mismatch() error {
	if err := r.Err(); err != nil {
		return err
	}
	return errTypeMismatch
}

// bytesSlice returns the elements of an array of strings, with nil for
// nil elements.
func (r *Reply) bytesSlice() ([][]byte, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, err
	}
	ret := make([][]byte, len(aa))
	for i := range aa {
		if aa[i].t == typeNil {
			continue
		}
		if ret[i], err = aa[i].Bytes(); err != nil {
			return nil, err
		}
	}
	return ret, nil
}

// Free resets Reply and put it back to memory pool.
// A Reply must not be used, or freed again, after Free.
func (r *Reply) Free() {
	if r == nil {
		return
	}
	r.release()
	if r.p {
		replyPool.Put(r)
	}
}

// release is Reset for a reply the package keeps for reuse, which also
// drops the array of r if it is larger than maxArrayPrealloc, so the reply
// does not pin the memory of one huge reply.
func (r *Reply) release() {
	r.reset(true)
}

// reset clears r, dropping its array as Reset or release describe, and
// returns the number of elements its arrays keep, nested ones included.
// The elements leave r.array[:len] here, where a later reset cannot see
// them, so their arrays are trimmed now.
func (r *Reply) reset(trim bool) int {
	r.t = typeUnset
	r.b = nil // never reuse it, it aliases the read buffer
	r.i = 0
	if len(r.array) < int(r.hide) && r.t != typeUnset {
		// a reply that is not an array was read since the last reset,
		// which reset twice in a row, as the helpers do, leaves alone
		r.unhide(len(r.array))
	}
	nested := 0
	for i := range r.array {
		nested += r.array[i].reset(true)
	}
	if nested > maxNested || trim && cap(r.array) > maxArrayPrealloc {
		r.array, r.hide = nil, 0
		return 0
	}
	r.array = r.array[:0]
	return cap(r.array) + nested
}

// unhide drops the nested arrays of the elements of r from n on, which the
// reply read into r does not reuse, as reset no longer sees them: so a Reply
// reused for many replies does not keep the arrays of each beyond maxNested.
// The array of r has been reset, so they are within its capacity, which is
// below hide if reading an array failed before it was filled.
func (r *Reply) unhide(n int) {
	if h := min(int(r.hide), cap(r.array)); h > n {
		hidden := r.array[n:h]
		for i := range hidden {
			hidden[i].array = nil
		}
	}
	r.hide = uint32(n)
}
