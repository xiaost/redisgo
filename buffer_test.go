package redisgo

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
)

func TestReader(t *testing.T) {
	buf := make([]byte, 0, 1<<20)
	for i := 0; i < 10000; i++ {
		b := make([]byte, 1+rand.Intn(100))
		for j := range b {
			b[j] = byte(rand.Intn(256))
		}
		buf = append(buf, b...)
		if i%100 == 0 {
			buf = append(buf, CR, LF)
		}
	}
	buf = append(buf, CR, LF)

	r0 := newReader(bytes.NewReader(buf), 10)
	buf0 := make([]byte, 0, 1<<20)
	for {
		b, err := r0.Readline()
		if err != nil {
			break
		}
		buf0 = append(buf0, b...)
	}
	if !bytes.Equal(buf, buf0) {
		t.Fatal("not equal", len(buf), len(buf0))
	}

	r1 := newReader(bytes.NewReader(buf), 10)
	buf1 := make([]byte, 0, 1<<20)
	for {
		b, err := r1.Read(1 + rand.Intn(50))
		if err != nil {
			buf1 = append(buf1, r1.bytes()...)
			break
		}
		buf1 = append(buf1, b...)
	}
	if !bytes.Equal(buf, buf1) {
		t.Fatal("not equal", len(buf), len(buf1), "\n", buf, "\n", buf1)
	}
}

// TestReaderLongLineGrowth checks that a long line arriving in small chunks
// grows the buffer geometrically instead of reallocating on every read.
func TestReaderLongLineGrowth(t *testing.T) {
	line := append(bytes.Repeat([]byte{'a'}, 20000), CR, LF)
	allocs := testing.AllocsPerRun(1, func() {
		r := newReader(&chunkConn{data: line, chunk: 100}, 2048)
		b, err := r.Readline()
		if err != nil || len(b) != len(line) {
			t.Fatal(len(b), err)
		}
	})
	// newReader allocates twice, then about log2(20000/2048) grows.
	if allocs > 8 {
		t.Fatalf("expect a handful of allocs, got %v", allocs)
	}
}

// TestReaderKeepsHandedOutBytes checks that growing never overwrites slices
// returned earlier, which Reply relies on for zero-copy results.
func TestReaderKeepsHandedOutBytes(t *testing.T) {
	var data []byte
	for i := 0; i < 500; i++ {
		data = append(data, "+hello\r\n"...)
	}
	r := newReader(&chunkConn{data: data, chunk: 7}, minReadBufferSize)
	var lines [][]byte
	for {
		b, err := r.Readline()
		if err != nil {
			break
		}
		lines = append(lines, b)
		if _, err := r.Read(0); err != nil {
			t.Fatal(err)
		}
	}
	if len(lines) != 500 {
		t.Fatal(len(lines))
	}
	for i, b := range lines {
		if string(b) != "+hello\r\n" {
			t.Fatalf("line %d corrupted: %q", i, b)
		}
	}
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) { return 0, nil }

func TestReaderNoProgress(t *testing.T) {
	r := newReader(emptyReader{}, 0)
	if _, err := r.Readline(); err != io.ErrNoProgress {
		t.Fatal(err)
	}
	if _, err := r.Read(5); err != io.ErrNoProgress {
		t.Fatal(err)
	}
}

func TestReaderEOF(t *testing.T) {
	r := newReader(bytes.NewReader([]byte("abc")), 0)
	if _, err := r.Read(5); err != io.EOF {
		t.Fatal(err)
	}
	r = newReader(bytes.NewReader([]byte("abc")), 0)
	if _, err := r.Readline(); err != io.EOF {
		t.Fatal(err)
	}
	r = newReader(bytes.NewReader([]byte("abc")), 0)
	if b, err := r.Read(3); err != nil || string(b) != "abc" {
		t.Fatal(b, err)
	}
}
