package redisgo

import "unsafe"

// ss converts b to a string without copying.
// The caller must not modify b afterwards.
func ss(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}
