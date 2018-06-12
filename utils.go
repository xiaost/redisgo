package redisgo

import (
	"unsafe"
)

func validarg(a interface{}) bool {
	switch a.(type) {
	case int, int8, int16, int32, int64:
	case uint, uint8, uint16, uint32, uint64:
	case float32, float64, []byte, string, []string:
	default:
		return false
	}
	return true
}

func ss(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}
