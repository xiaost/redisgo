package redisgo

import (
	"bufio"
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

func TestWriteCommandMatchesAppendCommand(t *testing.T) {
	big := strings.Repeat("x", maxCmdBuf+1024)
	cases := []struct {
		name string
		cmd  string
		args []any
	}{
		{"scalars", "CMD", []any{"", "abc", []byte("def"), []byte(nil),
			int8(-8), int16(-16), int32(-32), int64(math.MinInt64), math.MaxInt,
			uint8(8), uint16(16), uint32(32), uint64(math.MaxUint64), uint(math.MaxUint),
			float32(0.1), -math.MaxFloat64, math.SmallestNonzeroFloat64}},
		{"slices", "CMD", []any{
			[]string{"s1", "", "s2"},
			[][]byte{[]byte("b1"), nil, []byte("b2")},
			[]int{-1, 0, 1},
			[]int64{math.MinInt64, math.MaxInt64},
			[]uint64{0, math.MaxUint64},
			[]float64{1.5, -2.5},
			[]any{"nested", []any{int32(7), []string{"deep"}}},
		}},
		{"maps_single", "HSET", []any{"k",
			map[string]string{"f1": "v1"},
			map[string][]byte{"f2": []byte("v2")},
			map[string]any{"f3": int8(-1)},
			map[string]any{"f4": int16(-2)},
			map[string]any{"f5": int32(-3)},
			map[string]any{"f6": int64(-4)},
			map[string]any{"f7": uint(5)},
			map[string]any{"f8": uint8(6)},
			map[string]any{"f9": uint16(7)},
			map[string]any{"f10": uint32(8)},
			map[string]any{"f11": uint64(9)},
			map[string]any{"f12": float32(1.5)},
			map[string]any{"f13": float64(2.5)},
			map[string]any{"f14": []byte("bytes")},
			map[string]any{"f15": "str"},
		}},
		{"large", "SET", []any{"k", big}},
	}
	for _, bufSize := range []int{16, 256, 2048, maxCmdBuf} {
		for _, tc := range cases {
			n, size, err := argsInfo(tc.args)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			want := appendCommand(nil, tc.cmd, n, tc.args)
			if len(want) > cmdSize(tc.cmd, size) {
				t.Fatalf("%s: len %d exceeds cmdSize %d", tc.name, len(want), cmdSize(tc.cmd, size))
			}
			var sink bytes.Buffer
			bw := bufio.NewWriterSize(&sink, bufSize)
			if err := writeCommand(bw, tc.cmd, n, tc.args); err != nil {
				t.Fatalf("%s (buf=%d): %v", tc.name, bufSize, err)
			}
			if err := bw.Flush(); err != nil {
				t.Fatalf("%s (buf=%d): %v", tc.name, bufSize, err)
			}
			if !bytes.Equal(sink.Bytes(), want) {
				t.Fatalf("%s (buf=%d): writeCommand mismatch (got %dB, want %dB)", tc.name, bufSize, sink.Len(), len(want))
			}
		}
	}
}

func TestBulkSizeBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 9, 10, 99, 100, 999, 1000, 9999, 10000, 100000} {
		s := strings.Repeat("a", n)
		encoded := appendBulkString(nil, s)
		if got := bulkSize(n); got < len(encoded) {
			t.Fatalf("n=%d: bulkSize=%d < actual=%d", n, got, len(encoded))
		}
		if n < 10000 && bulkSize(n) > len(encoded)+3 {
			t.Fatalf("n=%d: bulkSize=%d too loose for actual=%d", n, bulkSize(n), len(encoded))
		}
	}
}

func TestSendStreamingAndFlushNoAlloc(t *testing.T) {
	// 128KB SET streams directly through bw with 0 allocs and keeps c.cmd nil.
	big := strings.Repeat("b", 128<<10)
	c := NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	allocs := testing.AllocsPerRun(50, func() {
		if err := c.DoNoReply("SET", "k", big); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("128KB SET: expect 0 allocs, got %v", allocs)
	}
	if c.cmd != nil {
		t.Fatalf("128KB SET should not allocate c.cmd, got cap=%d", cap(c.cmd))
	}

	// 50-int SADD fits in default 2KB write buffer with tight argsInfo (no c.cmd).
	ints := make([]int64, 50)
	for i := range ints {
		ints[i] = int64(i + 1000)
	}
	cInts := NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	if err := cInts.DoNoReply("SADD", "k", ints); err != nil {
		t.Fatal(err)
	}
	if cap(cInts.cmd) != 0 {
		t.Fatalf("50-int SADD should fit in default wbuf without c.cmd, got cap=%d", cap(cInts.cmd))
	}

	// Pipelining >2KB flushes bw instead of allocating c.cmd.
	cPipe := NewConn(&FakeConn{reply: []byte("+OK\r\n")}, WithReadTimeout(0), WithWriteTimeout(0))
	allocs = testing.AllocsPerRun(50, func() {
		for range 200 {
			if err := cPipe.Send("SET", "key", "value"); err != nil {
				t.Fatal(err)
			}
		}
		for range 200 {
			if err := cPipe.RecvNoReply(); err != nil {
				t.Fatal(err)
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("200-cmd pipeline send/recv: expect 0 allocs, got %v", allocs)
	}
	if cap(cPipe.cmd) != 0 {
		t.Fatalf("pipelining small commands should not allocate c.cmd, got cap=%d", cap(cPipe.cmd))
	}

	// Write error during streaming fails and closes conn cleanly.
	cf := NewConn(&failConn{}, WithReadTimeout(0), WithWriteTimeout(0))
	if err := cf.Send("SET", "k", big); err == nil || cf.Err() != err || !cf.closed {
		t.Fatalf("expected fatal write error on streaming fail, err=%v cerr=%v closed=%v", err, cf.Err(), cf.closed)
	}
}

type captureWriteConn struct {
	FakeConn
	buf    bytes.Buffer
	writes int
}

func (c *captureWriteConn) Write(b []byte) (int, error) {
	c.writes++
	return c.buf.Write(b)
}

func TestSendLargeCommandWriteCalls(t *testing.T) {
	val128K := strings.Repeat("a", 128<<10)
	val1M := strings.Repeat("b", 1<<20)
	ints4000 := make([]int, 4000)
	for i := range ints4000 {
		ints4000[i] = i
	}

	cases := []struct {
		name      string
		wbuf      int
		preSend   bool
		cmd       string
		args      []any
		maxWrites int
	}{
		{"128KB_default_wbuf", 2048, false, "SET", []any{"k", val128K}, 4},
		{"1MB_default_wbuf", 2048, false, "SET", []any{"k", val1M}, 18},
		{"1MB_small_wbuf_256", 256, false, "SET", []any{"k", val1M}, 18},
		{"128KB_tiny_wbuf_1", 1, false, "SET", []any{"k", val128K}, 4},
		{"128KB_large_wbuf_128K", 128 << 10, false, "SET", []any{"k", val128K}, 2},
		{"4000_ints_default_wbuf", 2048, false, "SADD", []any{"s", ints4000}, 2},
		{"128KB_with_buffered_prefix", 2048, true, "SET", []any{"k", val128K}, 5},
	}

	for _, tc := range cases {
		wc := &captureWriteConn{FakeConn: FakeConn{reply: []byte("+OK\r\n")}}
		c := NewConn(wc, WithWriteBuffer(tc.wbuf), WithReadTimeout(0), WithWriteTimeout(0))
		var want bytes.Buffer
		if tc.preSend {
			if err := c.Send("PING"); err != nil {
				t.Fatalf("%s: preSend: %v", tc.name, err)
			}
			want.Write(appendCommand(nil, "PING", 0, nil))
		}
		if err := c.Send(tc.cmd, tc.args...); err != nil {
			t.Fatalf("%s: Send: %v", tc.name, err)
		}
		if err := c.Flush(); err != nil {
			t.Fatalf("%s: Flush: %v", tc.name, err)
		}
		n, _, err := argsInfo(tc.args)
		if err != nil {
			t.Fatalf("%s: argsInfo: %v", tc.name, err)
		}
		want.Write(appendCommand(nil, tc.cmd, n, tc.args))
		if !bytes.Equal(wc.buf.Bytes(), want.Bytes()) {
			t.Fatalf("%s: wire encoding mismatch (got %dB, want %dB)", tc.name, wc.buf.Len(), want.Len())
		}
		if c.cmd != nil {
			t.Fatalf("%s: expected c.cmd == nil after >maxCmdBuf send, got cap=%d", tc.name, cap(c.cmd))
		}
		if wc.writes > tc.maxWrites {
			t.Errorf("%s: got %d conn.Write calls, want <= %d", tc.name, wc.writes, tc.maxWrites)
		}
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
