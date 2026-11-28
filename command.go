package redisgo

import (
	"bufio"
	"strconv"
	"sync"
)

const (
	// maxNumLen is the longest text produced for a numeric arg:
	// "-1.7976931348623157e+308" is 24 bytes, int64 and uint64 need at most 20.
	maxNumLen = 24
	// argOverhead is the RESP framing of one bulk string whose length has at
	// most 20 digits: '$' + len + CRLF + CRLF. The '*' header of a command
	// fits in it as well.
	argOverhead = 1 + 20 + 2 + 2
	// Tight upper bounds for encoded numeric bulk strings: "$<len>\r\n<digits>\r\n".
	maxInt32Bulk = 1 + 2 + 2 + 11 + 2 // "-2147483648"
	maxIntBulk   = 1 + 2 + 2 + 20 + 2 // "-9223372036854775808" / max uint64
	maxFloatBulk = 1 + 2 + 2 + 24 + 2 // "-1.7976931348623157e+308"
	// maxCmdBuf bounds the scratch buffer a Conn keeps for commands that do
	// not fit into its bufio.Writer.
	maxCmdBuf = 64 << 10
	// maxArgDepth bounds nested []any flattening so cyclic slices do not
	// overflow the stack.
	maxArgDepth = 8
)

// bulkSize returns a tight upper bound of the RESP bulk string byte length
// for a payload of n bytes.
func bulkSize(n int) int {
	if n < 10000 {
		return n + 9
	}
	return n + argOverhead
}

// argsInfo validates args and returns the number of bulk strings they
// expand to and an upper bound of their encoded size.
//
// Supported types:
//   - string, []byte
//   - int, int8, int16, int32, int64
//   - uint, uint8, uint16, uint32, uint64
//   - float32, float64
//   - []string, [][]byte, []int, []int64, []uint64, []float64, []any
//   - map[string]string, map[string][]byte, map[string]any (scalar values)
func argsInfo(args []any) (n, size int, err error) {
	for _, a := range args {
		switch v := a.(type) {
		case string:
			n++
			size += bulkSize(len(v))
		case []byte:
			n++
			size += bulkSize(len(v))
		case []string:
			n += len(v)
			for _, s := range v {
				size += bulkSize(len(s))
			}
		case int, int64, uint, uint64:
			n++
			size += maxIntBulk
		case int8, int16, int32, uint8, uint16, uint32:
			n++
			size += maxInt32Bulk
		case float32, float64:
			n++
			size += maxFloatBulk
		default:
			cn, cs, err := argInfoExtra(a, 0)
			if err != nil {
				return 0, 0, err
			}
			n += cn
			size += cs
		}
	}
	return n, size, nil
}

// flatArgs wraps a caller's []any slice so internal wrappers (such as
// Conn.scanPage and Script) can prepend fixed arguments in a stack-allocated
// []any without adding an extra []any nesting level or escaping the slice.
type flatArgs []any

func argInfoExtra(a any, depth int) (n, size int, err error) {
	switch v := a.(type) {
	case flatArgs:
		return argsInfo(v)
	case [][]byte:
		n = len(v)
		for _, x := range v {
			size += bulkSize(len(x))
		}
	case []int:
		n = len(v)
		size = maxIntBulk * len(v)
	case []int64:
		n = len(v)
		size = maxIntBulk * len(v)
	case []uint64:
		n = len(v)
		size = maxIntBulk * len(v)
	case []float64:
		n = len(v)
		size = maxFloatBulk * len(v)
	case []any:
		if depth >= maxArgDepth {
			return 0, 0, ErrInvalidArgType
		}
		for _, elem := range v {
			cn, cs, err := argInfoElem(elem, depth+1)
			if err != nil {
				return 0, 0, err
			}
			n += cn
			size += cs
		}
	case map[string]string:
		n = 2 * len(v)
		for k, val := range v {
			size += bulkSize(len(k)) + bulkSize(len(val))
		}
	case map[string][]byte:
		n = 2 * len(v)
		for k, val := range v {
			size += bulkSize(len(k)) + bulkSize(len(val))
		}
	case map[string]any:
		n = 2 * len(v)
		for k, val := range v {
			vsz, err := scalarArgSize(val)
			if err != nil {
				return 0, 0, err
			}
			size += bulkSize(len(k)) + vsz
		}
	default:
		return 0, 0, ErrInvalidArgType
	}
	return n, size, nil
}

func argInfoElem(a any, depth int) (int, int, error) {
	if sz, err := scalarArgSize(a); err == nil {
		return 1, sz, nil
	}
	if s, ok := a.([]string); ok {
		size := 0
		for _, x := range s {
			size += bulkSize(len(x))
		}
		return len(s), size, nil
	}
	return argInfoExtra(a, depth)
}

func scalarArgSize(a any) (int, error) {
	switch v := a.(type) {
	case string:
		return bulkSize(len(v)), nil
	case []byte:
		return bulkSize(len(v)), nil
	case int, int64, uint, uint64:
		return maxIntBulk, nil
	case int8, int16, int32, uint8, uint16, uint32:
		return maxInt32Bulk, nil
	case float32, float64:
		return maxFloatBulk, nil
	default:
		return 0, ErrInvalidArgType
	}
}

// cmdSize returns an upper bound of the encoded size of a command,
// given the size bound of its args from argsInfo.
func cmdSize(cmd string, argsize int) int {
	return argOverhead + bulkSize(len(cmd)) + argsize
}

// appendCommand appends the RESP encoding of cmd and args to b.
// n is the number of bulk strings args expand to, as returned by argsInfo,
// which must have validated args already.
func appendCommand(b []byte, cmd string, n int, args []any) []byte {
	b = append(b, '*')
	b = strconv.AppendInt(b, int64(n+1), 10)
	b = append(b, CR, LF)
	b = appendBulkString(b, cmd)
	return appendArgs(b, args)
}

func appendArgs(b []byte, args []any) []byte {
	for _, a := range args {
		switch v := a.(type) {
		case string:
			b = appendBulkString(b, v)
		case []byte:
			b = appendBulkString(b, ss(v))
		case []string:
			for _, s := range v {
				b = appendBulkString(b, s)
			}
		case int:
			b = appendBulkInt(b, int64(v))
		case int8:
			b = appendBulkInt(b, int64(v))
		case int16:
			b = appendBulkInt(b, int64(v))
		case int32:
			b = appendBulkInt(b, int64(v))
		case int64:
			b = appendBulkInt(b, v)
		case uint:
			b = appendBulkUint(b, uint64(v))
		case uint8:
			b = appendBulkUint(b, uint64(v))
		case uint16:
			b = appendBulkUint(b, uint64(v))
		case uint32:
			b = appendBulkUint(b, uint64(v))
		case uint64:
			b = appendBulkUint(b, v)
		case float32:
			b = appendBulkFloat(b, float64(v), 32)
		case float64:
			b = appendBulkFloat(b, v, 64)
		default:
			b = appendArgExtra(b, a)
		}
	}
	return b
}

func appendArgExtra(b []byte, a any) []byte {
	switch v := a.(type) {
	case flatArgs:
		return appendArgs(b, v)
	case [][]byte:
		for _, x := range v {
			b = appendBulkString(b, ss(x))
		}
	case []int:
		for _, x := range v {
			b = appendBulkInt(b, int64(x))
		}
	case []int64:
		for _, x := range v {
			b = appendBulkInt(b, x)
		}
	case []uint64:
		for _, x := range v {
			b = appendBulkUint(b, x)
		}
	case []float64:
		for _, x := range v {
			b = appendBulkFloat(b, x, 64)
		}
	case []any:
		b = appendArgs(b, v)
	case map[string]string:
		for k, val := range v {
			b = appendBulkString(b, k)
			b = appendBulkString(b, val)
		}
	case map[string][]byte:
		for k, val := range v {
			b = appendBulkString(b, k)
			b = appendBulkString(b, ss(val))
		}
	case map[string]any:
		for k, val := range v {
			b = appendBulkString(b, k)
			b = appendScalarArg(b, val)
		}
	}
	return b
}

func appendScalarArg(b []byte, a any) []byte {
	switch v := a.(type) {
	case string:
		return appendBulkString(b, v)
	case []byte:
		return appendBulkString(b, ss(v))
	case int:
		return appendBulkInt(b, int64(v))
	case int8:
		return appendBulkInt(b, int64(v))
	case int16:
		return appendBulkInt(b, int64(v))
	case int32:
		return appendBulkInt(b, int64(v))
	case int64:
		return appendBulkInt(b, v)
	case uint:
		return appendBulkUint(b, uint64(v))
	case uint8:
		return appendBulkUint(b, uint64(v))
	case uint16:
		return appendBulkUint(b, uint64(v))
	case uint32:
		return appendBulkUint(b, uint64(v))
	case uint64:
		return appendBulkUint(b, v)
	case float32:
		return appendBulkFloat(b, float64(v), 32)
	case float64:
		return appendBulkFloat(b, v, 64)
	}
	return b
}

func appendBulkString(b []byte, s string) []byte {
	b = append(b, '$')
	b = strconv.AppendInt(b, int64(len(s)), 10)
	b = append(b, CR, LF)
	b = append(b, s...)
	return append(b, CR, LF)
}

func appendBulkInt(b []byte, i int64) []byte {
	var tmp [maxNumLen]byte
	return appendBulkString(b, ss(strconv.AppendInt(tmp[:0], i, 10)))
}

func appendBulkUint(b []byte, u uint64) []byte {
	var tmp [maxNumLen]byte
	return appendBulkString(b, ss(strconv.AppendUint(tmp[:0], u, 10)))
}

func appendBulkFloat(b []byte, f float64, bits int) []byte {
	var tmp [maxNumLen]byte
	return appendBulkString(b, ss(strconv.AppendFloat(tmp[:0], f, 'g', -1, bits)))
}

var cmdChunkPool = sync.Pool{
	New: func() any {
		return new([maxCmdBuf]byte)
	},
}

// cmdWriter streams the RESP encoding of commands larger than maxCmdBuf.
// When bw's own buffer is smaller than maxCmdBuf, it batches output through a
// pooled maxCmdBuf chunk so large commands do not turn into many small writes
// on the underlying net.Conn, while keeping args non-escaping and c.cmd nil.
type cmdWriter struct {
	bw  *bufio.Writer
	buf []byte
	n   int
}

// writeCommand streams the RESP encoding of cmd and args through bw without
// retaining a scratch buffer on Conn. All argument bytes are copied into bw or
// a pooled chunk buffer so args never escape.
func writeCommand(bw *bufio.Writer, cmd string, n int, args []any) error {
	var cw cmdWriter
	cw.bw = bw
	var bp *[maxCmdBuf]byte
	if bw.Size() < maxCmdBuf {
		bp = cmdChunkPool.Get().(*[maxCmdBuf]byte)
		cw.buf = bp[:]
	}
	err := cw.writeCommand(cmd, n, args)
	if bp != nil {
		cmdChunkPool.Put(bp)
	}
	return err
}

func (w *cmdWriter) writeCommand(cmd string, n int, args []any) error {
	var hdr [argOverhead]byte
	b := append(hdr[:0], '*')
	b = strconv.AppendInt(b, int64(n+1), 10)
	b = append(b, CR, LF)
	if err := w.write(ss(b)); err != nil {
		return err
	}
	if err := w.writeBulkString(cmd); err != nil {
		return err
	}
	if err := w.writeArgs(args); err != nil {
		return err
	}
	return w.flush()
}

func (w *cmdWriter) flush() error {
	if w.n == 0 {
		return nil
	}
	_, err := w.bw.Write(w.buf[:w.n])
	w.n = 0
	return err
}

func (w *cmdWriter) write(s string) error {
	if w.buf == nil {
		return writeRaw(w.bw, s)
	}
	for len(s) > 0 {
		if w.n == len(w.buf) {
			if err := w.flush(); err != nil {
				return err
			}
		}
		n := copy(w.buf[w.n:], s)
		w.n += n
		s = s[n:]
	}
	return nil
}

func writeRaw(bw *bufio.Writer, s string) error {
	for len(s) > 0 {
		if bw.Available() == 0 {
			if err := bw.Flush(); err != nil {
				return err
			}
		}
		dst := bw.AvailableBuffer()
		n := copy(dst[:cap(dst)], s)
		if _, err := bw.Write(dst[:n]); err != nil {
			return err
		}
		s = s[n:]
	}
	return nil
}

func (w *cmdWriter) writeBulkString(s string) error {
	if w.buf != nil && len(s) <= len(w.buf)-w.n-argOverhead {
		w.n = len(appendBulkString(w.buf[:w.n], s))
		return nil
	}
	var hdr [argOverhead]byte
	b := append(hdr[:0], '$')
	b = strconv.AppendInt(b, int64(len(s)), 10)
	b = append(b, CR, LF)
	if err := w.write(ss(b)); err != nil {
		return err
	}
	if err := w.write(s); err != nil {
		return err
	}
	return w.write(CRLF)
}

func (w *cmdWriter) writeBulkInt(i int64) error {
	if w.buf != nil && len(w.buf)-w.n >= maxIntBulk {
		w.n = len(appendBulkInt(w.buf[:w.n], i))
		return nil
	}
	var tmp [maxIntBulk]byte
	return w.write(ss(appendBulkInt(tmp[:0], i)))
}

func (w *cmdWriter) writeBulkUint(u uint64) error {
	if w.buf != nil && len(w.buf)-w.n >= maxIntBulk {
		w.n = len(appendBulkUint(w.buf[:w.n], u))
		return nil
	}
	var tmp [maxIntBulk]byte
	return w.write(ss(appendBulkUint(tmp[:0], u)))
}

func (w *cmdWriter) writeBulkFloat(f float64, bits int) error {
	if w.buf != nil && len(w.buf)-w.n >= maxFloatBulk {
		w.n = len(appendBulkFloat(w.buf[:w.n], f, bits))
		return nil
	}
	var tmp [maxFloatBulk]byte
	return w.write(ss(appendBulkFloat(tmp[:0], f, bits)))
}

func (w *cmdWriter) writeArgs(args []any) error {
	for _, a := range args {
		var err error
		switch v := a.(type) {
		case string:
			err = w.writeBulkString(v)
		case []byte:
			err = w.writeBulkString(ss(v))
		case []string:
			for _, s := range v {
				if err = w.writeBulkString(s); err != nil {
					return err
				}
			}
		case int:
			err = w.writeBulkInt(int64(v))
		case int8:
			err = w.writeBulkInt(int64(v))
		case int16:
			err = w.writeBulkInt(int64(v))
		case int32:
			err = w.writeBulkInt(int64(v))
		case int64:
			err = w.writeBulkInt(v)
		case uint:
			err = w.writeBulkUint(uint64(v))
		case uint8:
			err = w.writeBulkUint(uint64(v))
		case uint16:
			err = w.writeBulkUint(uint64(v))
		case uint32:
			err = w.writeBulkUint(uint64(v))
		case uint64:
			err = w.writeBulkUint(v)
		case float32:
			err = w.writeBulkFloat(float64(v), 32)
		case float64:
			err = w.writeBulkFloat(v, 64)
		default:
			err = w.writeArgExtra(a)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (w *cmdWriter) writeArgExtra(a any) error {
	switch v := a.(type) {
	case flatArgs:
		return w.writeArgs(v)
	case [][]byte:
		for _, x := range v {
			if err := w.writeBulkString(ss(x)); err != nil {
				return err
			}
		}
	case []int:
		for _, x := range v {
			if err := w.writeBulkInt(int64(x)); err != nil {
				return err
			}
		}
	case []int64:
		for _, x := range v {
			if err := w.writeBulkInt(x); err != nil {
				return err
			}
		}
	case []uint64:
		for _, x := range v {
			if err := w.writeBulkUint(x); err != nil {
				return err
			}
		}
	case []float64:
		for _, x := range v {
			if err := w.writeBulkFloat(x, 64); err != nil {
				return err
			}
		}
	case []any:
		return w.writeArgs(v)
	case map[string]string:
		for k, val := range v {
			if err := w.writeBulkString(k); err != nil {
				return err
			}
			if err := w.writeBulkString(val); err != nil {
				return err
			}
		}
	case map[string][]byte:
		for k, val := range v {
			if err := w.writeBulkString(k); err != nil {
				return err
			}
			if err := w.writeBulkString(ss(val)); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, val := range v {
			if err := w.writeBulkString(k); err != nil {
				return err
			}
			if err := w.writeScalarArg(val); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *cmdWriter) writeScalarArg(a any) error {
	switch v := a.(type) {
	case string:
		return w.writeBulkString(v)
	case []byte:
		return w.writeBulkString(ss(v))
	case int:
		return w.writeBulkInt(int64(v))
	case int8:
		return w.writeBulkInt(int64(v))
	case int16:
		return w.writeBulkInt(int64(v))
	case int32:
		return w.writeBulkInt(int64(v))
	case int64:
		return w.writeBulkInt(v)
	case uint:
		return w.writeBulkUint(uint64(v))
	case uint8:
		return w.writeBulkUint(uint64(v))
	case uint16:
		return w.writeBulkUint(uint64(v))
	case uint32:
		return w.writeBulkUint(uint64(v))
	case uint64:
		return w.writeBulkUint(v)
	case float32:
		return w.writeBulkFloat(float64(v), 32)
	case float64:
		return w.writeBulkFloat(v, 64)
	}
	return nil
}
