package redisgo

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func tstr(s string) string {
	b := make([]byte, 0, 30+len(s))
	b = append(b, '$')
	b = strconv.AppendInt(b, int64(len(s)), 10)
	b = append(b, CRLF...)
	b = append(b, s...)
	b = append(b, CRLF...)
	return string(b)
}

func tcmd(aa ...string) string {
	s := strings.Join(aa, "")
	b := make([]byte, 0, 30+len(s))
	b = append(b, '*')
	b = strconv.AppendInt(b, int64(len(aa)), 10)
	b = append(b, CRLF...)
	b = append(b, s...)
	return string(b)
}

func TestCommand(t *testing.T) {
	buf := new(bytes.Buffer)
	c := commandPool.Get().(*command)
	c.Reset("CMD").Args("a0", []byte("a1")).Args(uint(1)).Dump(buf)
	if s := tcmd(tstr("CMD"), tstr("a0"), tstr("a1"), tstr("1")); s != buf.String() {
		t.Fatal("expect:\n", s, "get:\n", buf.String())
	}
}

func BenchmarkCommand(b *testing.B) {
	b.ReportAllocs()
	c := commandPool.Get().(*command)
	for i := 0; i < b.N; i++ {
		cmd := "GET"
		a1 := "key"
		a2 := 1
		c.Reset(cmd).Args(a1).Args(a2)
	}
}
