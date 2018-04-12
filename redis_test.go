package redisgo

import (
	"net"
	"testing"
	"time"
)

func TestRedis(t *testing.T) {
	conn, err := net.Dial("tcp", "127.0.0.1:6379")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cli := NewRedis(conn)
	if err := cli.Send("SET", "mykey", "myvalue", "PX", 50); err != nil {
		t.Fatal(err)
	}
	var reply Reply
	if err := cli.Recv(&reply); err != nil {
		t.Fatal(err)
	}
	if !reply.IsOK() {
		t.Fatal("set not ok")
	}

	resp, err := cli.Do("GET", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := resp.Bytes(); string(s) != "myvalue" {
		t.Fatal(s)
	}
	resp.Free()

	time.Sleep(55 * time.Millisecond)

	resp, err = cli.Do("GET", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if !resp.IsNil() && resp.Err() == ErrNil {
		t.Fatal("not nil", resp)
	}
	resp.Free()

	resp, err = cli.Do("XXX", "mykey")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Err() == nil {
		t.Fatal("resp err == nil")
	}
	t.Log(resp.Err())

	cli.Do("MGET", "x", "y")
}

func BenchmarkCmdSet(b *testing.B) {
	conn, err := net.Dial("tcp", "127.0.0.1:6379")
	if err != nil {
		b.Fatal(err)
	}
	defer conn.Close()
	cli := NewRedis(conn)
	var reply Reply
	for i := 0; i < b.N; i++ {
		if err := cli.Send("SET", "mykey", "myvalue", "PX", 50); err != nil {
			b.Fatal(err)
		}
		if err := cli.Recv(&reply); err != nil {
			b.Fatal(err)
		}
	}
}
