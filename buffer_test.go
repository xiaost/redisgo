package redisgo

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestReader(t *testing.T) {
	var b []byte
	buf := make([]byte, 0, 1<<20)
	for i := 0; i < 10000; i++ {
		b = make([]byte, 1+rand.Intn(100))
		rand.Read(b)
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
