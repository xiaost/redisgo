package redisgo

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type scriptedConn struct {
	chunkConn
	written bytes.Buffer
}

func (c *scriptedConn) Write(b []byte) (int, error) {
	c.written.Write(b)
	return len(b), nil
}

func TestParseDialURL(t *testing.T) {
	customTLS := &tls.Config{ServerName: "redis.example.com"}

	cases := []struct {
		url      string
		ops      []Option
		wantAddr string
		wantNet  string
		wantUser string
		wantPass string
		wantDB   int
		wantTLS  bool
	}{
		{"redis://127.0.0.1", nil, "127.0.0.1:6379", "tcp", "", "", 0, false},
		{"redis://:secret@10.0.0.1:6380/3", nil, "10.0.0.1:6380", "tcp", "", "secret", 3, false},
		{"redis://alice:secret@[::1]:6381/1", nil, "[::1]:6381", "tcp", "alice", "secret", 1, false},
		{"redis://[::1]/2", nil, "[::1]:6379", "tcp", "", "", 2, false},
		{"rediss://cache.internal", nil, "cache.internal:6379", "tcp", "", "", 0, true},
		{"rediss://cache.internal:6380?db=4", []Option{WithTLSConfig(customTLS), WithDB(5)}, "cache.internal:6380", "tcp", "", "", 5, true},
		{"unix:///tmp/redis.sock", nil, "/tmp/redis.sock", "unix", "", "", 0, false},
		{"unix://alice:pw@/var/run/redis.sock?db=2", nil, "/var/run/redis.sock", "unix", "alice", "pw", 2, false},
	}

	for _, tc := range cases {
		addr, o, err := parseDialURL(tc.url, tc.ops...)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", tc.url, err)
		}
		if addr != tc.wantAddr || o.network != tc.wantNet || o.username != tc.wantUser ||
			o.password != tc.wantPass || o.db != tc.wantDB || (o.tlsConfig != nil) != tc.wantTLS {
			t.Fatalf("%q: got addr=%q net=%q user=%q pass=%q db=%d tls=%v",
				tc.url, addr, o.network, o.username, o.password, o.db, o.tlsConfig != nil)
		}
	}

	bad := []string{
		"://bad",
		"http://127.0.0.1:6379",
		"redis://:6379",
		"redis://127.0.0.1/abc",
		"redis://127.0.0.1/-1",
		"redis://127.0.0.1/1/2",
		"redis://127.0.0.1/1?db=2",
		"unix://",
		"unix:///tmp/redis.sock?db=bad",
	}
	for _, u := range bad {
		if _, _, err := parseDialURL(u); err == nil {
			t.Errorf("%q: expected parse error", u)
		}
		if _, err := DialerURL(u); err == nil {
			t.Errorf("DialerURL(%q): expected error", u)
		}
	}
}

func TestInitConnHandshake(t *testing.T) {
	sc := &scriptedConn{chunkConn: chunkConn{data: []byte("+OK\r\n+OK\r\n+OK\r\n")}}
	o := applyOptions([]Option{
		WithReadTimeout(0),
		WithWriteTimeout(0),
		WithUsername("alice"),
		WithPassword("secret"),
		WithDB(3),
		WithClientName("app1"),
	})
	c, err := initConn(context.Background(), sc, o)
	if err != nil {
		t.Fatal(err)
	}
	want := tcmd(tstr("AUTH"), tstr("alice"), tstr("secret")) +
		tcmd(tstr("SELECT"), tstr("3")) +
		tcmd(tstr("CLIENT"), tstr("SETNAME"), tstr("app1"))
	if sc.written.String() != want {
		t.Fatalf("unexpected handshake commands:\nwant: %q\ngot:  %q", want, sc.written.String())
	}
	// Ensure Dialer handshake state is cleared by Pool as base state.
	p := NewPool(func(ctx context.Context) (*Conn, error) {
		return c, nil
	}, WithMaxIdle(1))
	defer p.Close()
	pc, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pc.Close()
	if p.Idle() != 1 {
		t.Fatalf("pooled conn from Dialer handshake should be reused, idle=%d", p.Idle())
	}

	// Password-only AUTH omits username.
	sc2 := &scriptedConn{chunkConn: chunkConn{data: []byte("+OK\r\n")}}
	c2, err := initConn(context.Background(), sc2, applyOptions([]Option{
		WithReadTimeout(0), WithWriteTimeout(0), WithPassword("pw"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	c2.Close()
	if got := sc2.written.String(); got != tcmd(tstr("AUTH"), tstr("pw")) {
		t.Fatalf("expect password-only AUTH, got %q", got)
	}
}

func TestInitConnHandshakeErrorClosesConn(t *testing.T) {
	sc := &scriptedConn{chunkConn: chunkConn{data: []byte("-WRONGPASS invalid\r\n+OK\r\n")}}
	_, err := initConn(context.Background(), sc, applyOptions([]Option{
		WithReadTimeout(0),
		WithWriteTimeout(0),
		WithPassword("bad"),
		WithDB(1),
	}))
	if err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("expect WRONGPASS error, got %v", err)
	}
	if !sc.closed {
		t.Fatal("failed handshake must close underlying net.Conn")
	}

	// Canceled ctx closes conn even when no handshake commands are needed.
	sc2 := &scriptedConn{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := initConn(ctx, sc2, defaultoptions); err != context.Canceled {
		t.Fatalf("expect Canceled, got %v", err)
	}
	if !sc2.closed {
		t.Fatal("canceled ctx must close underlying net.Conn")
	}
}

func TestInitConnCtxDeadline(t *testing.T) {
	client, server := net.Pipe()
	go io.Copy(io.Discard, server)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := initConn(ctx, client, applyOptions([]Option{WithPassword("pw")}))
	if err != context.DeadlineExceeded {
		t.Fatalf("expect DeadlineExceeded, got %v", err)
	}
}

func TestInitConnDialTimeoutAndCanceledWithAuth(t *testing.T) {
	// Canceled ctx must close the conn without sending AUTH.
	sc := &scriptedConn{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := initConn(ctx, sc, applyOptions([]Option{WithPassword("pw")})); err != context.Canceled {
		t.Fatalf("expect Canceled, got %v", err)
	}
	if !sc.closed || sc.written.Len() != 0 {
		t.Fatalf("canceled ctx wrote=%d closed=%v", sc.written.Len(), sc.closed)
	}

	// dialTimeout bounds stalling handshake even when read timeout is 0 and ctx has no deadline.
	client, server := net.Pipe()
	go io.Copy(io.Discard, server)
	_, err := initConn(context.Background(), client, applyOptions([]Option{
		WithReadTimeout(0),
		WithWriteTimeout(0),
		WithDialTimeout(25 * time.Millisecond),
		WithPassword("pw"),
	}))
	if !errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expect os.ErrDeadlineExceeded from dialTimeout, got %v", err)
	}
}

func TestOptionsHelpers(t *testing.T) {
	cfg := &tls.Config{ServerName: "a"}
	o := applyOptions([]Option{
		WithTimeout(12 * time.Second),
		WithNetwork(""),
		WithTLSConfig(cfg),
	})
	if o.rtimeout != 12*time.Second || o.wtimeout != 12*time.Second || o.network != "" {
		t.Fatalf("unexpected options: %+v", o)
	}
	// Dialer clones tlsConfig so later caller mutations cannot race.
	_ = Dialer("127.0.0.1:6379", WithTLSConfig(cfg))
	cfg.ServerName = "mutated"
	df, err := DialerURL("redis://127.0.0.1:6379", WithTLSConfig(&tls.Config{ServerName: "orig"}))
	if err != nil || df == nil {
		t.Fatal(df, err)
	}
}
