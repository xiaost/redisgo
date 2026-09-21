package redisgo

import (
	"bytes"
	"math"
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

// encode encodes a command the way Conn.Send does and checks the size bound.
func encode(t *testing.T, cmd string, args ...any) string {
	t.Helper()
	n, size, err := argsInfo(args)
	if err != nil {
		t.Fatal(err)
	}
	b := appendCommand(nil, cmd, n, args)
	if len(b) > cmdSize(cmd, size) {
		t.Fatalf("encoded %d bytes exceeds bound %d: %q", len(b), cmdSize(cmd, size), b)
	}
	if strings.Count(string(b), "\r\n") != 1+2*(n+1) {
		t.Fatalf("bulk count mismatch: %q", b)
	}
	return string(b)
}

func TestCommand(t *testing.T) {
	cases := []struct {
		cmd  string
		args []any
		want string
	}{
		{"CMD", []any{"a0", []byte("a1"), uint(1)}, tcmd(tstr("CMD"), tstr("a0"), tstr("a1"), tstr("1"))},
		{"CMD", []any{[]string{"a0", "a1"}, uint(1)}, tcmd(tstr("CMD"), tstr("a0"), tstr("a1"), tstr("1"))},
		{"CMD", []any{[]string{}}, tcmd(tstr("CMD"))},
		{"CMD", []any{"", []byte(nil)}, tcmd(tstr("CMD"), tstr(""), tstr(""))},
		{"CMD", []any{int8(-8), int16(-16), int32(-32), int64(math.MinInt64), math.MaxInt},
			tcmd(tstr("CMD"), tstr("-8"), tstr("-16"), tstr("-32"), tstr("-9223372036854775808"), tstr(strconv.Itoa(math.MaxInt)))},
		{"CMD", []any{uint8(8), uint16(16), uint32(32), uint64(math.MaxUint64), uint(math.MaxUint)},
			tcmd(tstr("CMD"), tstr("8"), tstr("16"), tstr("32"), tstr("18446744073709551615"), tstr(strconv.FormatUint(math.MaxUint, 10)))},
		{"CMD", []any{float32(0.1), 0.1, -math.MaxFloat64, math.SmallestNonzeroFloat64, float32(-math.MaxFloat32)},
			tcmd(tstr("CMD"), tstr("0.1"), tstr("0.1"), tstr("-1.7976931348623157e+308"), tstr("5e-324"), tstr("-3.4028235e+38"))},
	}
	for _, c := range cases {
		if got := encode(t, c.cmd, c.args...); got != c.want {
			t.Errorf("%s %v:\nexpect: %q\nget:    %q", c.cmd, c.args, c.want, got)
		}
	}
}

func TestCommandInvalidArg(t *testing.T) {
	for _, a := range []any{true, nil, 1i, struct{}{}, []int{1}, &struct{}{}} {
		if _, _, err := argsInfo([]any{"k", a}); err != errInvalidArgType {
			t.Errorf("%T: expect errInvalidArgType, got %v", a, err)
		}
	}
}

func TestSendInvalidArgKeepsConn(t *testing.T) {
	c := newTestConn("+PONG\r\n", 0)
	if err := c.Send("SET", "k", true); err != errInvalidArgType {
		t.Fatal(err)
	}
	if c.Err() != nil || c.pd != 0 || c.bw.Buffered() != 0 {
		t.Fatalf("conn poisoned: err=%v pd=%d buffered=%d", c.Err(), c.pd, c.bw.Buffered())
	}
	if b, err := c.DoBytes("PING"); err != nil || string(b) != "PONG" {
		t.Fatal(b, err)
	}
}

// TestSendLargeCommand exercises the slow path through the scratch buffer.
func TestSendLargeCommand(t *testing.T) {
	sink := new(bytes.Buffer)
	fc := &chunkConn{data: []byte("+OK\r\n"), FakeConn: FakeConn{}}
	c := NewConn(fc, WithWriteBuffer(256), WithReadTimeout(0), WithWriteTimeout(0))
	c.bw.Reset(sink)
	val := strings.Repeat("v", 1<<20)
	if err := c.Send("SET", "k", val); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	if sink.String() != tcmd(tstr("SET"), tstr("k"), tstr(val)) {
		t.Fatal("bad encoding of large command")
	}
	if c.cmd != nil {
		t.Fatalf("scratch buffer of %d bytes kept alive", cap(c.cmd))
	}
	sink.Reset()
	if err := c.Send("SET", "k", strings.Repeat("v", 300)); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(); err != nil {
		t.Fatal(err)
	}
	if cap(c.cmd) == 0 || cap(c.cmd) > maxCmdBuf {
		t.Fatalf("expect a small scratch buffer to be kept, cap=%d", cap(c.cmd))
	}
}

func BenchmarkCommand(b *testing.B) {
	b.ReportAllocs()
	buf := make([]byte, 0, 1024)
	for i := 0; i < b.N; i++ {
		cmd := "GET"
		a1 := "key"
		a2 := 1
		args := []any{a1, a2}
		n, _, _ := argsInfo(args)
		buf = appendCommand(buf[:0], cmd, n, args)
	}
}
