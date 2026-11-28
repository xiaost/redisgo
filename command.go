package redisgo

import (
	"strconv"
)

const (
	// maxNumLen is the longest text produced for a numeric arg:
	// "-1.7976931348623157e+308" is 24 bytes, int64 and uint64 need at most 20.
	maxNumLen = 24
	// argOverhead is the RESP framing of one bulk string whose length has at
	// most 20 digits: '$' + len + CRLF + CRLF. The '*' header of a command
	// fits in it as well.
	argOverhead = 1 + 20 + 2 + 2
	// maxCmdBuf bounds the scratch buffer a Conn keeps for commands that do
	// not fit into its bufio.Writer.
	maxCmdBuf = 64 << 10
	// maxArgDepth bounds nested []any flattening so cyclic slices do not
	// overflow the stack.
	maxArgDepth = 8
)

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
			size += argOverhead + len(v)
		case []byte:
			n++
			size += argOverhead + len(v)
		case []string:
			n += len(v)
			size += argOverhead * len(v)
			for _, s := range v {
				size += len(s)
			}
		case int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			n++
			size += argOverhead + maxNumLen
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

func argInfoExtra(a any, depth int) (n, size int, err error) {
	switch v := a.(type) {
	case [][]byte:
		n = len(v)
		size = argOverhead * len(v)
		for _, x := range v {
			size += len(x)
		}
	case []int:
		n = len(v)
		size = (argOverhead + maxNumLen) * len(v)
	case []int64:
		n = len(v)
		size = (argOverhead + maxNumLen) * len(v)
	case []uint64:
		n = len(v)
		size = (argOverhead + maxNumLen) * len(v)
	case []float64:
		n = len(v)
		size = (argOverhead + maxNumLen) * len(v)
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
		size = 2 * argOverhead * len(v)
		for k, val := range v {
			size += len(k) + len(val)
		}
	case map[string][]byte:
		n = 2 * len(v)
		size = 2 * argOverhead * len(v)
		for k, val := range v {
			size += len(k) + len(val)
		}
	case map[string]any:
		n = 2 * len(v)
		for k, val := range v {
			vsz, err := scalarArgSize(val)
			if err != nil {
				return 0, 0, err
			}
			size += argOverhead + len(k) + vsz
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
		size := argOverhead * len(s)
		for _, x := range s {
			size += len(x)
		}
		return len(s), size, nil
	}
	return argInfoExtra(a, depth)
}

func scalarArgSize(a any) (int, error) {
	switch v := a.(type) {
	case string:
		return argOverhead + len(v), nil
	case []byte:
		return argOverhead + len(v), nil
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return argOverhead + maxNumLen, nil
	default:
		return 0, ErrInvalidArgType
	}
}

// cmdSize returns an upper bound of the encoded size of a command,
// given the size bound of its args from argsInfo.
func cmdSize(cmd string, argsize int) int {
	return argOverhead + argOverhead + len(cmd) + argsize
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
