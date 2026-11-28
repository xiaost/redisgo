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
		{"CMD", []any{[][]byte{[]byte("a"), nil, []byte("b")}}, tcmd(tstr("CMD"), tstr("a"), tstr(""), tstr("b"))},
		{"CMD", []any{[]int{1, -2}}, tcmd(tstr("CMD"), tstr("1"), tstr("-2"))},
		{"CMD", []any{[]int64{10, -20}}, tcmd(tstr("CMD"), tstr("10"), tstr("-20"))},
		{"CMD", []any{[]uint64{10, 20}}, tcmd(tstr("CMD"), tstr("10"), tstr("20"))},
		{"CMD", []any{[]float64{1.5, -2.5}}, tcmd(tstr("CMD"), tstr("1.5"), tstr("-2.5"))},
		{"CMD", []any{[]any{"x", []any{1, []string{"y"}}}}, tcmd(tstr("CMD"), tstr("x"), tstr("1"), tstr("y"))},
		{"CMD", []any{map[string]string{"f1": "v1"}}, tcmd(tstr("CMD"), tstr("f1"), tstr("v1"))},
		{"CMD", []any{map[string][]byte{"f1": []byte("v1")}}, tcmd(tstr("CMD"), tstr("f1"), tstr("v1"))},
		{"CMD", []any{map[string]any{"f1": int64(42)}}, tcmd(tstr("CMD"), tstr("f1"), tstr("42"))},
		{"CMD", []any{map[string]string{}}, tcmd(tstr("CMD"))},
	}
	for _, c := range cases {
		if got := encode(t, c.cmd, c.args...); got != c.want {
			t.Errorf("%s %v:\nexpect: %q\nget:    %q", c.cmd, c.args, c.want, got)
		}
	}
}

func TestCommandMapMultiKeys(t *testing.T) {
	got := encode(t, "HSET", "k", map[string]any{
		"s": "str",
		"b": []byte("bin"),
		"i": 7,
		"f": 2.5,
	})
	// Map order is unspecified; verify exact header count and all key-value pairs appear.
	if !strings.HasPrefix(got, "*10\r\n$4\r\nHSET\r\n$1\r\nk\r\n") {
		t.Fatalf("unexpected HSET header: %q", got)
	}
	for _, pair := range []string{
		tstr("s") + tstr("str"),
		tstr("b") + tstr("bin"),
		tstr("i") + tstr("7"),
		tstr("f") + tstr("2.5"),
	} {
		if !strings.Contains(got, pair) {
			t.Fatalf("missing pair %q in %q", pair, got)
		}
	}
}

func TestCommandInvalidArg(t *testing.T) {
	cyc := []any{nil}
	cyc[0] = cyc
	deep := []any{"leaf"}
	for range maxArgDepth {
		deep = []any{deep}
	}
	for _, a := range []any{
		true, nil, 1i, struct{}{}, []struct{}{}, &struct{}{},
		map[string][]int{"k": {1}},
		map[string]any{"k": []int{1}},
		map[string]any{"k": true},
		cyc,
		deep,
	} {
		if _, _, err := argsInfo([]any{"k", a}); err != ErrInvalidArgType {
			t.Errorf("%T: expect ErrInvalidArgType, got %v", a, err)
		}
	}
}

func TestSendInvalidArgKeepsConn(t *testing.T) {
	c := newTestConn("+PONG\r\n", 0)
	if err := c.Send("SET", "k", true); err != ErrInvalidArgType {
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
