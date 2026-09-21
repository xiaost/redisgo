package redisgo

import "unsafe"

func validarg(a any) bool {
	switch a.(type) {
	case int, int8, int16, int32, int64:
	case uint, uint8, uint16, uint32, uint64:
	case float32, float64, []byte, string, []string:
	default:
		return false
	}
	return true
}

// ss converts b to a string without copying.
// The caller must not modify b afterwards.
func ss(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}
