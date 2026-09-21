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
)

// argsInfo validates args and returns the number of bulk strings they
// expand to and an upper bound of their encoded size.
//
// type must be one of:
// int, int8, int16, int32, int64
// uint, uint8, uint16, uint32, uint64
// float32, float64
// []byte, string, []string
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
			return 0, 0, errInvalidArgType
		}
	}
	return n, size, nil
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
		}
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
