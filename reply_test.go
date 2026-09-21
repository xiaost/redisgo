package redisgo

import (
	"io"
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
		if aa, err := r.Array(); aa != nil || err != nil || r.IsNil() {
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
	if r.err != nil || r.b != nil || len(r.array) != 0 || r.t != typeUnset {
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
