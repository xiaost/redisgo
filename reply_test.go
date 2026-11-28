package redisgo

import (
	"errors"
	"io"
	"math"
	"strconv"
	"strings"
	"testing"
)

// chunkConn serves a fixed RESP stream in reads of at most chunk bytes,
// so tests can exercise replies split across several reads.
type chunkConn struct {
	FakeConn
	data  []byte
	off   int
	chunk int
}

func (c *chunkConn) Read(b []byte) (int, error) {
	if c.off >= len(c.data) {
		return 0, io.EOF
	}
	if c.chunk > 0 && len(b) > c.chunk {
		b = b[:c.chunk]
	}
	n := copy(b, c.data[c.off:])
	c.off += n
	return n, nil
}

func newTestConn(resp string, chunk int) *Conn {
	return NewConn(&chunkConn{data: []byte(resp), chunk: chunk},
		WithReadTimeout(0), WithWriteTimeout(0))
}

func TestReplyParse(t *testing.T) {
	const resp = "+OK\r\n" +
		"-ERR boom\r\n" +
		":-42\r\n" +
		"$5\r\nhello\r\n" +
		"$0\r\n\r\n" +
		"$-1\r\n" +
		"*-1\r\n" +
		"*0\r\n" +
		"*3\r\n$1\r\na\r\n:1\r\n*2\r\n+x\r\n$-1\r\n"
	for _, chunk := range []int{0, 1, 3, 7} {
		c := newTestConn(resp, chunk)
		var r Reply
		recv := func() {
			if err := c.Recv(&r); err != nil {
				t.Fatalf("chunk=%d: %v", chunk, err)
			}
		}
		recv()
		if !r.IsOK() {
			t.Fatalf("chunk=%d: expect +OK", chunk)
		}
		recv()
		if err := r.Err(); err == nil || err.Error() != "ERR boom" {
			t.Fatalf("chunk=%d: expect ERR boom, got %v", chunk, err)
		}
		if _, ok := r.Err().(RedisErr); !ok {
			t.Fatalf("chunk=%d: expect RedisErr", chunk)
		}
		recv()
		if i, err := r.Integer(); err != nil || i != -42 {
			t.Fatalf("chunk=%d: expect -42, got %d %v", chunk, i, err)
		}
		recv()
		if b, err := r.Bytes(); err != nil || string(b) != "hello" {
			t.Fatalf("chunk=%d: expect hello, got %q %v", chunk, b, err)
		}
		recv()
		if b, err := r.Bytes(); err != nil || len(b) != 0 {
			t.Fatalf("chunk=%d: expect empty bulk, got %q %v", chunk, b, err)
		}
		recv()
		if !r.IsNil() || r.Err() != ErrNil {
			t.Fatalf("chunk=%d: expect nil bulk", chunk)
		}
		recv()
		if aa, err := r.Array(); aa != nil || err != ErrNil || !r.IsNil() || r.Err() != ErrNil {
			t.Fatalf("chunk=%d: expect nil array, got %v %v", chunk, aa, err)
		}
		recv()
		if aa, err := r.Array(); aa == nil || len(aa) != 0 || err != nil {
			t.Fatalf("chunk=%d: expect empty array, got %v %v", chunk, aa, err)
		}
		recv()
		aa, err := r.Array()
		if err != nil || len(aa) != 3 {
			t.Fatalf("chunk=%d: expect 3 elements, got %d %v", chunk, len(aa), err)
		}
		if b, _ := aa[0].Bytes(); string(b) != "a" {
			t.Fatalf("chunk=%d: expect a, got %q", chunk, b)
		}
		if i, _ := aa[1].Integer(); i != 1 {
			t.Fatalf("chunk=%d: expect 1, got %d", chunk, i)
		}
		inner, err := aa[2].Array()
		if err != nil || len(inner) != 2 {
			t.Fatalf("chunk=%d: expect nested 2 elements, got %d %v", chunk, len(inner), err)
		}
		if b, _ := inner[0].Bytes(); string(b) != "x" {
			t.Fatalf("chunk=%d: expect x, got %q", chunk, b)
		}
		if !inner[1].IsNil() {
			t.Fatalf("chunk=%d: expect nested nil", chunk)
		}
		if err := c.Recv(&r); err != io.EOF {
			t.Fatalf("chunk=%d: expect EOF, got %v", chunk, err)
		}
	}
}

func TestReplyMalformed(t *testing.T) {
	cases := []string{
		"$-2\r\n",
		"*-2\r\n",
		"$9223372036854775807\r\n",
		"*9223372036854775807\r\n",
		"$536870913\r\n", // maxLen + 1
		"$abc\r\n",
		"$1x\r\n",
		":1.5\r\n",
		"?\r\n",
		"\r\n",
		"$3\r\nabcde\r\n", // payload longer than declared
		"$+1\r\nx\r\n",
		"*+0\r\n",
		"*-0\r\n",
		"$-01\r\n",
		"*-2\r\n",
	}
	for _, in := range cases {
		c := newTestConn(in+"+OK\r\n", 0)
		var r Reply
		if err := c.Recv(&r); err != ErrProtocol {
			t.Errorf("%q: expect ErrProtocol, got %v", in, err)
		}
		if c.Err() == nil {
			t.Errorf("%q: conn should be marked broken", in)
		}
	}
}

func TestReplyResetClearsRefs(t *testing.T) {
	c := newTestConn("-ERR x\r\n*1\r\n$1\r\na\r\n", 0)
	r := NewReply()
	defer r.Free()
	if err := c.Recv(r); err != nil {
		t.Fatal(err)
	}
	if err := c.Recv(r); err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if r.b != nil || len(r.array) != 0 || r.t != typeUnset {
		t.Fatalf("Reset left state behind: %+v", r)
	}
	if cap(r.array) == 0 || r.array[:1][0].b != nil {
		t.Fatal("nested element not reset")
	}
}

func TestReplyReuseDoesNotAllocate(t *testing.T) {
	const one = "*2\r\n*2\r\n+a\r\n$1\r\nb\r\n*1\r\n:1\r\n"
	var resp string
	for i := 0; i < 32; i++ {
		resp += one
	}
	c := newTestConn(resp, 0)
	r := NewReply()
	defer r.Free()
	if err := c.Recv(r); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(16, func() {
		if err := c.Recv(r); err != nil {
			t.Fatal(err)
		}
		if aa, _ := r.Array(); len(aa) != 2 {
			t.Fatal("bad array")
		}
	})
	if allocs != 0 {
		t.Fatalf("expect 0 allocs when reusing a reply with nested arrays, got %v", allocs)
	}
}

func TestParseInt(t *testing.T) {
	ok := []string{"0", "-0", "+0", "7", "-1", "+1", "007", "123456789012345678", "-12345678901234567",
		"+12345678901234567", "1234567890123456789", "9223372036854775807", "-9223372036854775808",
		"+9223372036854775807"}
	for _, s := range ok {
		want, _ := strconv.ParseInt(s, 10, 64)
		if n, err := parseInt([]byte(s)); err != nil || n != want {
			t.Errorf("%q: expect %d, got %d %v", s, want, n, err)
		}
	}
	bad := []string{"", "-", "+", "+-1", "-+1", "1-", "--1", "++1", " 1", "1 ", "0x1", "1.0",
		"9223372036854775808", "-9223372036854775809", "+9223372036854775808"}
	for _, s := range bad {
		if n, err := parseInt([]byte(s)); err != ErrProtocol {
			t.Errorf("%q: expect ErrProtocol, got %d %v", s, n, err)
		}
	}
}

func TestReplyMaxDepth(t *testing.T) {
	nested := func(depth int) string {
		return strings.Repeat("*1\r\n", depth) + ":1\r\n"
	}
	c := newTestConn(nested(maxDepth)+"+OK\r\n", 0)
	var r Reply
	if err := c.Recv(&r); err != nil {
		t.Fatal(err)
	}
	if err := c.Recv(&r); err != nil || !r.IsOK() {
		t.Fatal(err)
	}
	// a stream nesting far beyond the stack limit must fail, not crash
	for _, depth := range []int{maxDepth + 1, 1 << 24} {
		c = newTestConn(nested(depth), 0)
		if err := c.Recv(&r); err != ErrProtocol {
			t.Fatalf("depth %d: expect ErrProtocol, got %v", depth, err)
		}
	}
}

// TestReleaseDropsNestedArrays checks that the scratch reply of a Conn does
// not keep a large array nested in an earlier reply.
func TestReleaseDropsNestedArrays(t *testing.T) {
	inner := make([]string, 2*maxArrayPrealloc)
	for i := range inner {
		inner[i] = ":1\r\n"
	}
	c := newTestConn(tcmd(tcmd(inner...))+"+OK\r\n", 0)
	if err := c.DoNoReply("EXEC"); err != nil {
		t.Fatal(err)
	}
	if aa := c.reply.array[:cap(c.reply.array)]; len(aa) == 0 || cap(aa[0].array) > maxArrayPrealloc {
		t.Fatalf("nested array of %d kept", cap(aa[0].array))
	}
	if err := c.DoNoReply("PING"); err != nil {
		t.Fatal(err)
	}
}

func TestRecvClosedKeepsReply(t *testing.T) {
	c := newTestConn("+OK\r\n", 0)
	var r Reply
	c.Send("PING")
	if err := c.Recv(&r); err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := c.Recv(&r); err != ErrClosed || !r.IsOK() {
		t.Fatal(err, r.IsOK())
	}
}

// TestResetDropsHiddenNestedArrays checks that a large array nested in a
// reply that is not the last one read into a Reply does not stay hidden
// beyond the length of the array.
func TestResetDropsHiddenNestedArrays(t *testing.T) {
	inner := make([]string, 2*maxArrayPrealloc)
	for i := range inner {
		inner[i] = ":1\r\n"
	}
	big := tcmd(tcmd(inner...))
	hidden := func(r *Reply) int {
		aa := r.array[:cap(r.array)]
		if len(aa) == 0 {
			return 0
		}
		return cap(aa[0].array)
	}
	// a pending reply dropped by Do
	c := newTestConn(big+"+PONG\r\n", 0)
	c.Send("EXEC")
	if _, err := c.DoString("PING"); err != nil {
		t.Fatal(err)
	}
	if n := hidden(&c.reply); n > maxArrayPrealloc {
		t.Errorf("Do: nested array of %d kept", n)
	}
	// a Reply reused by Recv
	c = newTestConn(big+"+PONG\r\n", 0)
	r := NewReply()
	defer r.Free()
	c.Send("EXEC")
	c.Send("PING")
	c.Recv(r)
	c.Recv(r)
	if n := hidden(r); n > maxArrayPrealloc {
		t.Errorf("Recv: nested array of %d kept", n)
	}
}

// TestReleaseBoundsNestedTotal checks that many nested arrays, each within
// maxArrayPrealloc, are not kept together.
func TestReleaseBoundsNestedTotal(t *testing.T) {
	child := make([]string, maxArrayPrealloc)
	for i := range child {
		child[i] = ":1\r\n"
	}
	children := make([]string, 64)
	for i := range children {
		children[i] = tcmd(child...)
	}
	c := newTestConn(tcmd(children...)+"+OK\r\n", 0)
	if err := c.DoNoReply("EXEC"); err != nil {
		t.Fatal(err)
	}
	kept := 0
	for _, e := range c.reply.array[:cap(c.reply.array)] {
		kept += cap(e.array)
	}
	if kept > maxNested {
		t.Fatalf("%d nested elements kept", kept)
	}
	if err := c.DoNoReply("PING"); err != nil {
		t.Fatal(err)
	}
}

// retained counts the elements the arrays of r keep, hidden ones included.
func retained(r *Reply) int {
	n := cap(r.array)
	for _, e := range r.array[:cap(r.array)] {
		n += retained(&e)
	}
	return n
}

// TestResetBoundsAcrossReplies checks that the bound of nested arrays holds
// for a Reply reused for many replies, each within it, where the elements
// that are not reused by a shorter reply keep the arrays of a former one.
func TestResetBoundsAcrossReplies(t *testing.T) {
	child := make([]string, maxArrayPrealloc)
	for i := range child {
		child[i] = ":1\r\n"
	}
	heavy := tcmd(child...)
	var replies strings.Builder
	for k := 64; k > 0; k-- {
		// k elements, the last 3 heavy: within the bound on its own
		elems := make([]string, k)
		for i := range elems {
			elems[i] = ":1\r\n"
			if i >= k-3 {
				elems[i] = heavy
			}
		}
		replies.WriteString(tcmd(elems...))
		replies.WriteString(":1\r\n") // a scalar in between
	}
	c := newTestConn(replies.String(), 0)
	r := NewReply()
	defer r.Free()
	most := 0
	for range 128 {
		c.Send("X")
		if err := c.Recv(r); err != nil {
			t.Fatal(err)
		}
		r.Reset()
		most = max(most, retained(r))
	}
	if most > 2*maxNested {
		t.Fatalf("%d elements kept by a reused Reply", most)
	}
}

// TestResetAfterFailedArrayRead checks that a Reply is still safe to reset
// and reuse after reading a large array failed half way.
func TestResetAfterFailedArrayRead(t *testing.T) {
	r := NewReply()
	defer r.Free()
	c := newTestConn("*5000\r\n:1\r\n:2\r\n", 0) // truncated
	c.Send("X")
	if err := c.Recv(r); err == nil {
		t.Fatal("expect an error for a truncated array")
	}
	r.Reset()
	c = newTestConn("*2\r\n*1\r\n:1\r\n:2\r\n+OK\r\n", 0)
	c.Send("X")
	c.Send("Y")
	if err := c.Recv(r); err != nil {
		t.Fatal(err)
	}
	if err := c.Recv(r); err != nil || !r.IsOK() {
		t.Fatal(err)
	}
}

func TestRedisErrHasPrefix(t *testing.T) {
	err := RedisErr("NOSCRIPT No matching script.")
	if !err.HasPrefix("NOSCRIPT") || err.HasPrefix("WRONGTYPE") {
		t.Fatalf("unexpected HasPrefix result for %q", err)
	}
	if err.HasPrefix("NOSCRIPT No matching script. EXTRA") {
		t.Fatal("prefix longer than error matched")
	}
	if !RedisErr("ERR").HasPrefix("") {
		t.Fatal("empty prefix should match")
	}
}

func TestReplyScalarGetters(t *testing.T) {
	resp := ":42\r\n" +
		":-7\r\n" +
		tstr("1234567890123") +
		tstr("-99") +
		"+1\r\n" +
		tstr("3.25") +
		tstr("inf") +
		tstr("true") +
		tstr("0") +
		tstr("hello") +
		"$-1\r\n" +
		"-ERR boom\r\n" +
		"*0\r\n"
	c := newTestConn(resp, 0)
	var r Reply
	mustRecv := func() {
		if err := c.Recv(&r); err != nil {
			t.Fatal(err)
		}
	}

	mustRecv() // :42
	if v, err := r.Int(); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if v, err := r.Int64(); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if v, err := r.Uint64(); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if v, err := r.Float64(); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	if v, err := r.Bool(); err != nil || !v {
		t.Fatal(v, err)
	}
	if _, err := r.String(); err != ErrTypeMismatch {
		t.Fatal(err)
	}

	mustRecv() // :-7
	if v, err := r.Int64(); err != nil || v != -7 {
		t.Fatal(v, err)
	}
	if _, err := r.Uint64(); !errors.Is(err, strconv.ErrRange) {
		t.Fatalf("expect ErrRange for negative Uint64, got %v", err)
	}

	mustRecv() // $13\r\n1234567890123
	if v, err := r.Int64(); err != nil || v != 1234567890123 {
		t.Fatal(v, err)
	}
	if v, err := r.Uint64(); err != nil || v != 1234567890123 {
		t.Fatal(v, err)
	}
	if v, err := r.String(); err != nil || v != "1234567890123" {
		t.Fatal(v, err)
	}

	mustRecv() // "-99"
	if v, err := r.Int(); err != nil || v != -99 {
		t.Fatal(v, err)
	}
	if _, err := r.Uint64(); !errors.Is(err, strconv.ErrSyntax) {
		t.Fatal(err)
	}

	mustRecv() // +1
	if v, err := r.Bool(); err != nil || !v {
		t.Fatal(v, err)
	}
	if v, err := r.Int64(); err != nil || v != 1 {
		t.Fatal(v, err)
	}

	mustRecv() // "3.25"
	if v, err := r.Float64(); err != nil || v != 3.25 {
		t.Fatal(v, err)
	}

	mustRecv() // "inf"
	if v, err := r.Float64(); err != nil || !math.IsInf(v, 1) {
		t.Fatal(v, err)
	}

	mustRecv() // "true"
	if v, err := r.Bool(); err != nil || !v {
		t.Fatal(v, err)
	}

	mustRecv() // "0"
	if v, err := r.Bool(); err != nil || v {
		t.Fatal(v, err)
	}

	mustRecv() // "hello"
	if v, err := r.String(); err != nil || v != "hello" {
		t.Fatal(v, err)
	}

	mustRecv() // $-1
	if _, err := r.String(); err != ErrNil {
		t.Fatal(err)
	}
	if _, err := r.Int64(); err != ErrNil {
		t.Fatal(err)
	}
	if _, err := r.Uint64(); err != ErrNil {
		t.Fatal(err)
	}
	if _, err := r.Float64(); err != ErrNil {
		t.Fatal(err)
	}
	if _, err := r.Bool(); err != ErrNil {
		t.Fatal(err)
	}

	mustRecv() // -ERR boom
	if _, err := r.String(); err == nil || err.Error() != "ERR boom" {
		t.Fatal(err)
	}
	if _, err := r.Int(); err == nil || err.Error() != "ERR boom" {
		t.Fatal(err)
	}

	mustRecv() // *0
	if _, err := r.Float64(); err != ErrTypeMismatch {
		t.Fatal(err)
	}
}

func TestReplyParseErrorDoesNotAliasBuffer(t *testing.T) {
	// Send invalid numbers in bulk strings via Conn helpers so the read buffer
	// is reclaimed and overwritten by subsequent replies; returned errors must stay intact.
	const bad = "not_a_number_1234567890"
	c := newTestConn(
		tstr(bad)+tstr("ok")+
			tstr(bad)+tstr("ok")+
			tstr(bad)+tstr("ok")+
			tstr(bad)+tstr("ok"),
		0,
	)
	check := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), bad) {
			t.Fatalf("%s: unexpected err %v", name, err)
		}
		if s, okErr := c.DoString("GET", "k"); okErr != nil || s != "ok" {
			t.Fatal(s, okErr)
		}
		if !strings.Contains(err.Error(), bad) {
			t.Fatalf("%s: error message corrupted after buffer reuse: %q", name, err.Error())
		}
	}
	_, err := c.DoInt64("GET", "k")
	check("Int64", err)
	_, err = c.DoUint64("GET", "k")
	check("Uint64", err)
	_, err = c.DoFloat64("GET", "k")
	check("Float64", err)
	_, err = c.DoBool("GET", "k")
	check("Bool", err)
}

func TestReplySliceAndMapGetters(t *testing.T) {
	smallStrings := make([]string, 10)
	for i := range smallStrings {
		smallStrings[i] = tstr("item-" + strconv.Itoa(i))
	}
	bigVal := strings.Repeat("x", maxPackBytes+100)
	bigKey := strings.Repeat("k", maxPackBytes+100)
	resp := "*-1\r\n" +
		"*0\r\n" +
		tcmd(tstr("a"), "$-1\r\n", tstr(""), tstr("b")) +
		tcmd(smallStrings...) +
		tcmd(tstr(bigVal), tstr("tail")) +
		tcmd(":1\r\n", tstr("2"), ":-3\r\n") +
		tcmd(tstr("1.5"), ":2\r\n", tstr("-0.25")) +
		tcmd(":1\r\n", ":0\r\n", tstr("true"), tstr("false")) +
		tcmd(tstr("k1"), tstr("v1"), tstr("k2"), "$-1\r\n") +
		tcmd(tstr("k1"), tstr(bigVal), tstr("k2"), tstr("v2")) +
		tcmd(tstr("c1"), ":10\r\n", tstr("c2"), tstr("-5")) +
		tcmd(tstr(bigKey), ":42\r\n") +
		tcmd(tstr("only_key")) +
		tcmd(tstr("k"), "-ERR bad\r\n") +
		tcmd(tstr("k"), "$-1\r\n")

	c := newTestConn(resp, 0)
	var r Reply
	mustRecv := func() {
		if err := c.Recv(&r); err != nil {
			t.Fatal(err)
		}
	}

	mustRecv() // *-1
	if _, err := r.Strings(); err != ErrNil {
		t.Fatal(err)
	}
	if _, err := r.StringMap(); err != ErrNil {
		t.Fatal(err)
	}
	if _, err := r.Int64Map(); err != ErrNil {
		t.Fatal(err)
	}

	mustRecv() // *0
	if ss, err := r.Strings(); err != nil || ss == nil || len(ss) != 0 {
		t.Fatal(ss, err)
	}
	if m, err := r.StringMap(); err != nil || m == nil || len(m) != 0 {
		t.Fatal(m, err)
	}
	if m, err := r.Int64Map(); err != nil || m == nil || len(m) != 0 {
		t.Fatal(m, err)
	}

	mustRecv() // ["a", nil, "", "b"]
	if ss, err := r.Strings(); err != nil || len(ss) != 4 || ss[0] != "a" || ss[1] != "" || ss[2] != "" || ss[3] != "b" {
		t.Fatal(ss, err)
	}

	mustRecv() // packed 10 strings
	if ss, err := r.Strings(); err != nil || len(ss) != 10 || ss[9] != "item-9" {
		t.Fatal(ss, err)
	}

	mustRecv() // unpacked > maxPackBytes
	if ss, err := r.Strings(); err != nil || len(ss) != 2 || ss[0] != bigVal || ss[1] != "tail" {
		t.Fatal(len(ss), err)
	}

	mustRecv() // Ints / Int64s
	if ii, err := r.Ints(); err != nil || len(ii) != 3 || ii[0] != 1 || ii[1] != 2 || ii[2] != -3 {
		t.Fatal(ii, err)
	}
	if ii, err := r.Int64s(); err != nil || len(ii) != 3 || ii[2] != -3 {
		t.Fatal(ii, err)
	}

	mustRecv() // Float64s
	if ff, err := r.Float64s(); err != nil || len(ff) != 3 || ff[0] != 1.5 || ff[1] != 2 || ff[2] != -0.25 {
		t.Fatal(ff, err)
	}

	mustRecv() // Bools
	if bb, err := r.Bools(); err != nil || len(bb) != 4 || !bb[0] || bb[1] || !bb[2] || bb[3] {
		t.Fatal(bb, err)
	}

	mustRecv() // StringMap packed
	if m, err := r.StringMap(); err != nil || len(m) != 2 || m["k1"] != "v1" || m["k2"] != "" {
		t.Fatal(m, err)
	}

	mustRecv() // StringMap > maxPackBytes
	if m, err := r.StringMap(); err != nil || len(m) != 2 || m["k1"] != bigVal || m["k2"] != "v2" {
		t.Fatal(len(m), err)
	}

	mustRecv() // Int64Map
	if m, err := r.Int64Map(); err != nil || len(m) != 2 || m["c1"] != 10 || m["c2"] != -5 {
		t.Fatal(m, err)
	}

	mustRecv() // Int64Map > maxPackBytes
	if m, err := r.Int64Map(); err != nil || len(m) != 1 || m[bigKey] != 42 {
		t.Fatal(m, err)
	}

	mustRecv() // odd length map
	if _, err := r.StringMap(); err != ErrTypeMismatch {
		t.Fatal(err)
	}
	if _, err := r.Int64Map(); err != ErrTypeMismatch {
		t.Fatal(err)
	}

	mustRecv() // error element inside array
	if _, err := r.StringMap(); err == nil || err.Error() != "ERR bad" {
		t.Fatal(err)
	}
	if _, err := r.Int64Map(); err == nil || err.Error() != "ERR bad" {
		t.Fatal(err)
	}

	mustRecv() // nil value inside Int64Map
	if _, err := r.Int64Map(); err != ErrNil {
		t.Fatal(err)
	}
}

func TestReplyStringsPackAllocs(t *testing.T) {
	elems := make([]string, 10)
	for i := range elems {
		elems[i] = tstr("value-" + strconv.Itoa(i))
	}
	c := newTestConn(strings.Repeat(tcmd(elems...), 200), 0)
	allocs := testing.AllocsPerRun(50, func() {
		ss, err := c.DoStrings("MGET", "k")
		if err != nil || len(ss) != 10 {
			t.Fatal(len(ss), err)
		}
	})
	if allocs != 2 {
		t.Fatalf("expect 2 allocs (slice + packed buffer), got %v", allocs)
	}
}

func TestReplyScan(t *testing.T) {
	resp := tcmd(tstr("alice"), ":30\r\n", ":-99\r\n", tstr("99"), tstr("1.5"), tstr("true"), tstr("raw"), "$-1\r\n", "$0\r\n\r\n", tstr("skipme")) +
		tcmd(tcmd(tstr("x"), tstr("y")), tcmd(tstr("a"), tstr("1")), tcmd(":1\r\n", ":2\r\n"), tcmd(":3\r\n", tstr("4")), tcmd(tstr("1.2")), tcmd(":1\r\n"), tcmd(tstr("f"), tstr("v")), tcmd(tstr("n"), ":7\r\n")) +
		tstr("scalar") +
		"$-1\r\n" +
		"-ERR fail\r\n" +
		tcmd(tstr("onlyone")) +
		tcmd(tstr("skip"), "-ERR elem\r\n")

	c := newTestConn(resp, 0)
	var r Reply
	mustRecv := func() {
		if err := c.Recv(&r); err != nil {
			t.Fatal(err)
		}
	}

	mustRecv()
	var (
		name     string
		age      int
		i64      int64
		u64      uint64
		score    float64
		active   bool
		raw      = make([]byte, 0, 16)
		miss     = "preset"
		emptyBuf []byte
	)
	if err := r.Scan(&name, &age, &i64, &u64, &score, &active, &raw, &miss, &emptyBuf, nil); err != nil {
		t.Fatal(err)
	}
	if name != "alice" || age != 30 || i64 != -99 || u64 != 99 || score != 1.5 || !active ||
		string(raw) != "raw" || miss != "" || emptyBuf == nil || len(emptyBuf) != 0 {
		t.Fatalf("unexpected Scan values: %q %d %d %d %v %v %q %q %v",
			name, age, i64, u64, score, active, raw, miss, emptyBuf)
	}

	mustRecv()
	var (
		ss   []string
		bb   [][]byte
		ii   []int
		ii64 []int64
		ff   []float64
		bo   []bool
		sm   map[string]string
		im   map[string]int64
	)
	if err := r.Scan(&ss, &bb, &ii, &ii64, &ff, &bo, &sm, &im); err != nil {
		t.Fatal(err)
	}
	if len(ss) != 2 || ss[1] != "y" || len(bb) != 2 || string(bb[0]) != "a" || len(ii) != 2 || ii[1] != 2 ||
		len(ii64) != 2 || ii64[1] != 4 || len(ff) != 1 || ff[0] != 1.2 || len(bo) != 1 || !bo[0] || sm["f"] != "v" || im["n"] != 7 {
		t.Fatal(ss, bb, ii, ii64, ff, bo, sm, im)
	}

	// Ensure *[]byte and *[][]byte copy rather than aliasing the connection read buffer.
	for i := range c.br.b[:c.br.r] {
		c.br.b[i] = 'Z'
	}
	if string(raw) != "raw" || string(bb[0]) != "a" {
		t.Fatal("Scan byte slices aliased buffer")
	}

	mustRecv() // scalar
	if err := r.Scan(&name); err != nil || name != "scalar" {
		t.Fatal(name, err)
	}
	if err := r.Scan(&name, &i64); err != ErrTypeMismatch {
		t.Fatal(err)
	}
	if err := r.Scan(name); err != ErrInvalidArgType {
		t.Fatal(err)
	}
	var nilPtr *int
	if err := r.Scan(nilPtr); err != ErrInvalidArgType {
		t.Fatal(err)
	}

	mustRecv() // $-1
	if err := r.Scan(&name); err != ErrNil {
		t.Fatal(err)
	}

	mustRecv() // -ERR fail
	if err := r.Scan(&name); err == nil || err.Error() != "ERR fail" {
		t.Fatal(err)
	}

	mustRecv() // ["onlyone"]
	if err := r.Scan(&name, &age); err != ErrTypeMismatch {
		t.Fatal(err)
	}

	mustRecv() // ["skip", -ERR elem]
	if err := r.Scan(nil, nil); err == nil || err.Error() != "ERR elem" {
		t.Fatal(err)
	}
	if string(raw) != "raw" || string(bb[0]) != "a" {
		t.Fatal(raw, bb[0])
	}
}

// TestResetUnhidesAfterScalar checks the Reply.reset fix where a scalar reply
// read after an array reply trims hidden nested arrays left beyond len(array).
func TestResetUnhidesAfterScalar(t *testing.T) {
	inner := make([]string, maxArrayPrealloc)
	for i := range inner {
		inner[i] = ":1\r\n"
	}
	c := newTestConn(tcmd(tcmd(inner...))+":1\r\n", 0)
	r := NewReply()
	defer r.Free()
	if err := c.Recv(r); err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if retained(r) == 0 {
		t.Fatal("expected nested array to be kept after array reply")
	}
	if err := c.Recv(r); err != nil {
		t.Fatal(err)
	}
	r.Reset()
	aa := r.array[:cap(r.array)]
	if len(aa) > 0 && aa[0].array != nil {
		t.Fatalf("hidden nested array of %d not cleared after scalar reply", cap(aa[0].array))
	}
}
