package redisgo

import (
	"math"
	"strconv"
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

// maxPackBytes bounds the size of each backing buffer allocated when packing
// strings or byte slices in Strings, StringMap, Int64Map, and Scan so
// retaining one element never pins more than 4KB.
const maxPackBytes = 4 << 10

// maxPackElem bounds an individual element eligible for chunk packing; larger
// elements get their own exact allocation so they neither waste chunk space
// nor stay pinned to unrelated small elements.
const maxPackElem = maxPackBytes / 2

// emptyBytes is a non-nil, zero-capacity slice returned for empty bulk/simple
// strings so callers do not pin the Conn read buffer and append never aliases.
var emptyBytes = []byte{}

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

func (r *Reply) ok() (bool, error) {
	if r.IsOK() {
		return true, nil
	}
	if r.t == typeNil {
		return false, nil
	}
	return false, r.mismatch()
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

func constSimpleString(b []byte) (string, bool) {
	switch ss(b) {
	case "OK":
		return "OK", true
	case "PONG":
		return "PONG", true
	case "QUEUED":
		return "QUEUED", true
	}
	return "", false
}

// aliasesBuf reports whether any field of r holds non-empty bytes aliasing
// the Conn read buffer.
func (r *Reply) aliasesBuf() bool {
	switch r.t {
	case typeSString, typeBString, typeError:
		return len(r.b) > 0
	case typeArray:
		return r.arrayAliasesBuf()
	}
	return false
}

func (r *Reply) arrayAliasesBuf() bool {
	for i := range r.array {
		if r.array[i].aliasesBuf() {
			return true
		}
	}
	return false
}

// String returns a copy of "Simple Strings" or "Bulk Strings" as a string.
func (r *Reply) String() (string, error) {
	switch r.t {
	case typeBString:
		return string(r.b), nil
	case typeSString:
		if s, ok := constSimpleString(r.b); ok {
			return s, nil
		}
		return string(r.b), nil
	}
	return "", r.mismatch()
}

// Integer returns int64 of integer protocol:
// https://redis.io/topics/protocol#resp-integers
func (r *Reply) Integer() (int64, error) {
	if r.t != typeInteger {
		return 0, r.mismatch()
	}
	return r.i, nil
}

// Int64 returns the reply as an int64, accepting RESP integers as well as
// simple and bulk strings formatted as base-10 integers.
func (r *Reply) Int64() (int64, error) {
	switch r.t {
	case typeInteger:
		return r.i, nil
	case typeSString, typeBString:
		if n, err := parseInt(r.b); err == nil {
			return n, nil
		}
		// Copy on error so *strconv.NumError.Num never aliases the read buffer.
		return strconv.ParseInt(string(r.b), 10, 64)
	}
	return 0, r.mismatch()
}

// Int returns the reply as an int, accepting RESP integers as well as
// simple and bulk strings formatted as base-10 integers.
func (r *Reply) Int() (int, error) {
	n, err := r.Int64()
	if err != nil {
		return 0, err
	}
	if n < math.MinInt || n > math.MaxInt {
		return 0, &strconv.NumError{Func: "ParseInt", Num: strconv.FormatInt(n, 10), Err: strconv.ErrRange}
	}
	return int(n), nil
}

// Uint64 returns the reply as a uint64, accepting non-negative RESP integers
// as well as simple and bulk strings formatted as unsigned base-10 integers.
func (r *Reply) Uint64() (uint64, error) {
	switch r.t {
	case typeInteger:
		if r.i < 0 {
			return 0, &strconv.NumError{Func: "ParseUint", Num: strconv.FormatInt(r.i, 10), Err: strconv.ErrRange}
		}
		return uint64(r.i), nil
	case typeSString, typeBString:
		u, err := strconv.ParseUint(ss(r.b), 10, 64)
		if err != nil {
			return strconv.ParseUint(string(r.b), 10, 64)
		}
		return u, nil
	}
	return 0, r.mismatch()
}

// Float64 returns the reply as a float64, accepting RESP integers as well as
// simple and bulk strings parsed by strconv.ParseFloat.
func (r *Reply) Float64() (float64, error) {
	switch r.t {
	case typeSString, typeBString:
		f, err := strconv.ParseFloat(ss(r.b), 64)
		if err != nil {
			return strconv.ParseFloat(string(r.b), 64)
		}
		return f, nil
	case typeInteger:
		return float64(r.i), nil
	}
	return 0, r.mismatch()
}

// Bool returns the reply as a bool, accepting RESP integers (non-zero is true)
// as well as simple and bulk strings parsed by strconv.ParseBool.
func (r *Reply) Bool() (bool, error) {
	switch r.t {
	case typeInteger:
		return r.i != 0, nil
	case typeSString, typeBString:
		v, err := strconv.ParseBool(ss(r.b))
		if err != nil {
			return strconv.ParseBool(string(r.b))
		}
		return v, nil
	}
	return false, r.mismatch()
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
	return ErrTypeMismatch
}

// BytesSlice returns the elements of an array of strings, with nil for
// nil elements. The returned slices alias the read buffer of the Conn, see Conn.
func (r *Reply) BytesSlice() ([][]byte, error) {
	bb, _, err := r.bytesSlice()
	return bb, err
}

func (r *Reply) bytesSlice() ([][]byte, bool, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, false, err
	}
	ret := make([][]byte, len(aa))
	exposed := false
	for i := range aa {
		if aa[i].t == typeNil {
			continue
		}
		b, err := aa[i].Bytes()
		if err != nil {
			return nil, false, err
		}
		if len(b) > 0 {
			exposed = true
		}
		ret[i] = b
	}
	return ret, exposed, nil
}

// stringPacker packs small strings (length in [2, maxPackElem]) into <= maxPackBytes
// backing buffers so multi-bulk replies avoid per-element heap allocations while
// bounding the memory any single retained string can pin.
type stringPacker struct {
	buf []byte
	off int
	rem int
}

func (p *stringPacker) add(r *Reply) {
	if r.t == typeSString {
		if _, ok := constSimpleString(r.b); ok {
			return
		}
	}
	if n := len(r.b); n >= 2 && n <= maxPackElem {
		p.rem += n
	}
}

func (p *stringPacker) grow() {
	p.buf = make([]byte, min(p.rem, maxPackBytes))
	p.off = 0
}

func (p *stringPacker) next(b []byte) string {
	n := len(b)
	if n <= 1 || n > maxPackElem {
		return string(b)
	}
	if n > len(p.buf)-p.off {
		p.grow()
	}
	copy(p.buf[p.off:], b)
	s := ss(p.buf[p.off : p.off+n])
	p.off += n
	p.rem -= n
	return s
}

func (p *stringPacker) nextSimple(b []byte) string {
	if s, ok := constSimpleString(b); ok {
		return s
	}
	return p.next(b)
}

func (p *stringPacker) nextReply(r *Reply) string {
	if r.t == typeBString {
		return p.next(r.b)
	}
	return p.nextSimple(r.b)
}

// bytesPacker packs copied byte slices into <= maxPackBytes buffers with cap==len
// per slice so appending to one element cannot overwrite its neighbor.
type bytesPacker struct {
	buf []byte
	off int
	rem int
}

func (p *bytesPacker) next(b []byte) []byte {
	n := len(b)
	if n == 0 {
		return emptyBytes
	}
	if n > maxPackElem {
		c := make([]byte, n)
		copy(c, b)
		return c
	}
	if n > len(p.buf)-p.off {
		p.buf = make([]byte, min(p.rem, maxPackBytes))
		p.off = 0
	}
	dst := p.buf[p.off : p.off+n : p.off+n]
	copy(dst, b)
	p.off += n
	p.rem -= n
	return dst
}

func (r *Reply) bytesSliceCopy() ([][]byte, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, err
	}
	var bp bytesPacker
	for i := range aa {
		if aa[i].t == typeNil {
			continue
		}
		b, err := aa[i].Bytes()
		if err != nil {
			return nil, err
		}
		if len(b) > 0 && len(b) <= maxPackElem {
			bp.rem += len(b)
		}
	}
	ret := make([][]byte, len(aa))
	for i := range aa {
		if aa[i].t == typeNil {
			continue
		}
		ret[i] = bp.next(aa[i].b)
	}
	return ret, nil
}

// Strings returns the elements of an array of strings, with "" for nil elements.
func (r *Reply) Strings() ([]string, error) {
	return r.appendStrings(nil)
}

func (r *Reply) appendStrings(dst []string) ([]string, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, err
	}
	var sp stringPacker
	for i := range aa {
		if aa[i].t == typeNil {
			continue
		}
		if _, err := aa[i].Bytes(); err != nil {
			return nil, err
		}
		sp.add(&aa[i])
	}
	if len(aa) == 0 {
		if dst == nil {
			return []string{}, nil
		}
		return dst[:0], nil
	}
	if cap(dst) < len(aa) {
		dst = make([]string, len(aa))
	} else {
		dst = dst[:len(aa)]
	}
	for i := range aa {
		if aa[i].t == typeNil {
			dst[i] = ""
			continue
		}
		if aa[i].t == typeBString {
			dst[i] = sp.next(aa[i].b)
		} else {
			dst[i] = sp.nextSimple(aa[i].b)
		}
	}
	return dst, nil
}

func parseSlice[T any](r *Reply, fn func(*Reply) (T, error)) ([]T, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, err
	}
	ret := make([]T, len(aa))
	for i := range aa {
		v, err := fn(&aa[i])
		if err != nil {
			return nil, err
		}
		ret[i] = v
	}
	return ret, nil
}

// Ints returns the elements of an array parsed with Int.
func (r *Reply) Ints() ([]int, error) {
	return parseSlice(r, (*Reply).Int)
}

// Int64s returns the elements of an array parsed with Int64.
func (r *Reply) Int64s() ([]int64, error) {
	return parseSlice(r, (*Reply).Int64)
}

// Float64s returns the elements of an array parsed with Float64.
func (r *Reply) Float64s() ([]float64, error) {
	return parseSlice(r, (*Reply).Float64)
}

// Bools returns the elements of an array parsed with Bool.
func (r *Reply) Bools() ([]bool, error) {
	return parseSlice(r, (*Reply).Bool)
}

// StringMap returns a flat field/value array reply (such as HGETALL or
// CONFIG GET) as a map[string]string. Nil values become "".
func (r *Reply) StringMap() (map[string]string, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, err
	}
	if len(aa)%2 != 0 {
		return nil, ErrTypeMismatch
	}
	var sp stringPacker
	for i := 0; i < len(aa); i += 2 {
		if _, err := aa[i].Bytes(); err != nil {
			return nil, err
		}
		sp.add(&aa[i])
		if aa[i+1].t != typeNil {
			if _, err := aa[i+1].Bytes(); err != nil {
				return nil, err
			}
			sp.add(&aa[i+1])
		}
	}
	m := make(map[string]string, len(aa)/2)
	for i := 0; i < len(aa); i += 2 {
		var ks, vs string
		if aa[i].t == typeBString {
			ks = sp.next(aa[i].b)
		} else {
			ks = sp.nextSimple(aa[i].b)
		}
		if aa[i+1].t == typeBString {
			vs = sp.next(aa[i+1].b)
		} else if aa[i+1].t != typeNil {
			vs = sp.nextSimple(aa[i+1].b)
		}
		m[ks] = vs
	}
	return m, nil
}

// Int64Map returns a flat field/value array reply as a map[string]int64.
func (r *Reply) Int64Map() (map[string]int64, error) {
	aa, err := r.Array()
	if err != nil {
		return nil, err
	}
	if len(aa)%2 != 0 {
		return nil, ErrTypeMismatch
	}
	var sp stringPacker
	for i := 0; i < len(aa); i += 2 {
		if _, err := aa[i].Bytes(); err != nil {
			return nil, err
		}
		vt := aa[i+1].t
		if vt != typeInteger && vt != typeSString && vt != typeBString {
			return nil, aa[i+1].mismatch()
		}
		sp.add(&aa[i])
	}
	m := make(map[string]int64, len(aa)/2)
	for i := 0; i < len(aa); i += 2 {
		v, err := aa[i+1].Int64()
		if err != nil {
			return nil, err
		}
		var ks string
		if aa[i].t == typeBString {
			ks = sp.next(aa[i].b)
		} else {
			ks = sp.nextSimple(aa[i].b)
		}
		m[ks] = v
	}
	return m, nil
}

func (r *Reply) scanResult() (uint64, []string, error) {
	return r.scanResultInto(nil)
}

func (r *Reply) scanResultInto(dst []string) (uint64, []string, error) {
	aa, err := r.Array()
	if err != nil {
		return 0, nil, err
	}
	if len(aa) != 2 {
		return 0, nil, ErrTypeMismatch
	}
	cursor, err := aa[0].Uint64()
	if err != nil {
		return 0, nil, err
	}
	items, err := aa[1].appendStrings(dst)
	if err != nil {
		return 0, nil, err
	}
	return cursor, items, nil
}

// Scan unpacks a scalar or array reply into pointer destinations dest.
//
// Supported destination types are *string, *[]byte, *int, *int64, *uint64,
// *float64, *bool, *[]string, *[][]byte, *[]int, *[]int64, *[]float64,
// *[]bool, *map[string]string, and *map[string]int64. Passing nil skips the
// corresponding array element (after checking for a server error).
//
// For array replies, nil elements set the destination to its zero value;
// if len(dest) < len(array), only the first len(dest) elements are scanned.
// A top-level nil reply returns ErrNil. Byte slices copied into *[]byte and
// *[][]byte do not alias the connection read buffer.
func (r *Reply) Scan(dest ...any) error {
	if err := r.Err(); err != nil {
		return err
	}
	if r.t == typeUnset {
		return ErrTypeMismatch
	}
	if r.t == typeArray {
		if len(dest) > len(r.array) {
			return ErrTypeMismatch
		}
		var sp stringPacker
		if len(dest) >= 2 {
			strCount := 0
			for i, dst := range dest {
				if d, ok := dst.(*string); ok && d != nil {
					elem := &r.array[i]
					if elem.t == typeBString || elem.t == typeSString {
						prev := sp.rem
						sp.add(elem)
						if sp.rem != prev {
							strCount++
						}
					}
				}
			}
			if strCount < 2 {
				sp.rem = 0
			}
		}
		for i, dst := range dest {
			if err := scanValue(&r.array[i], dst, true, &sp); err != nil {
				return err
			}
		}
		return nil
	}
	if len(dest) != 1 {
		return ErrTypeMismatch
	}
	return scanValue(r, dest[0], false, nil)
}

func scanValue(r *Reply, dst any, allowNil bool, sp *stringPacker) error {
	if dst == nil {
		if r.t == typeError {
			return RedisErr(r.b)
		}
		return nil
	}
	isNil := allowNil && r.t == typeNil
	switch d := dst.(type) {
	case *string:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = ""
			return nil
		}
		if sp != nil && sp.rem > 0 && (r.t == typeBString || r.t == typeSString) {
			*d = sp.nextReply(r)
			return nil
		}
		v, err := r.String()
		if err != nil {
			return err
		}
		*d = v
	case *[]byte:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		b, err := r.Bytes()
		if err != nil {
			return err
		}
		if len(b) == 0 {
			if *d == nil {
				*d = emptyBytes
			} else {
				*d = (*d)[:0]
			}
			return nil
		}
		if cap(*d) >= len(b) {
			*d = (*d)[:len(b)]
			copy(*d, b)
		} else {
			*d = append([]byte(nil), b...)
		}
	case *int:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = 0
			return nil
		}
		v, err := r.Int()
		if err != nil {
			return err
		}
		*d = v
	case *int64:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = 0
			return nil
		}
		v, err := r.Int64()
		if err != nil {
			return err
		}
		*d = v
	case *uint64:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = 0
			return nil
		}
		v, err := r.Uint64()
		if err != nil {
			return err
		}
		*d = v
	case *float64:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = 0
			return nil
		}
		v, err := r.Float64()
		if err != nil {
			return err
		}
		*d = v
	case *bool:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = false
			return nil
		}
		v, err := r.Bool()
		if err != nil {
			return err
		}
		*d = v
	case *[]string:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.Strings()
		if err != nil {
			return err
		}
		*d = v
	case *[][]byte:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		bb, err := r.bytesSliceCopy()
		if err != nil {
			return err
		}
		*d = bb
	case *[]int:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.Ints()
		if err != nil {
			return err
		}
		*d = v
	case *[]int64:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.Int64s()
		if err != nil {
			return err
		}
		*d = v
	case *[]float64:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.Float64s()
		if err != nil {
			return err
		}
		*d = v
	case *[]bool:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.Bools()
		if err != nil {
			return err
		}
		*d = v
	case *map[string]string:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.StringMap()
		if err != nil {
			return err
		}
		*d = v
	case *map[string]int64:
		if d == nil {
			return ErrInvalidArgType
		}
		if isNil {
			*d = nil
			return nil
		}
		v, err := r.Int64Map()
		if err != nil {
			return err
		}
		*d = v
	default:
		return ErrInvalidArgType
	}
	return nil
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
	prev := r.t
	r.t = typeUnset
	r.b = nil // never reuse it, it aliases the read buffer
	r.i = 0
	if len(r.array) < int(r.hide) && prev != typeUnset {
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
