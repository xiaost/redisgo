package redisgo

import (
	"bytes"
	"testing"
)

func TestCommand(t *testing.T) {
	buf := new(bytes.Buffer)
	c := commandPool.Get().(*command)
	c.Reset("CMD").Args("a0", []byte("a1")).Args(uint(1)).Dump(buf)
	if buf.String() != "*4\r\n$3\r\nCMD\r\n$2\r\na0\r\n$2\r\na1\r\n$1\r\n1\r\n" {
		t.Fatal(buf.String())
	}
}

func BenchmarkCommand(b *testing.B) {
	c := commandPool.Get().(*command)
	for i := 0; i < b.N; i++ {
		cmd := "GET"
		a1 := "key"
		a2 := 1
		c.Reset(cmd).Args(a1).Args(a2)
	}
}
