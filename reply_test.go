package redisgo

import (
	"io"
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
		if err := c.Recv(&r); err != errProtocol {
			t.Errorf("%q: expect errProtocol, got %v", in, err)
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
		if n, err := parseInt([]byte(s)); err != errProtocol {
			t.Errorf("%q: expect errProtocol, got %d %v", s, n, err)
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
		if err := c.Recv(&r); err != errProtocol {
			t.Fatalf("depth %d: expect errProtocol, got %v", depth, err)
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
