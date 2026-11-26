# redisgo

[![GoDoc](https://godoc.org/github.com/xiaost/redisgo?status.svg)](https://godoc.org/github.com/xiaost/redisgo)

A high performance and simple redis client for Go(golang).

It is inspired by [redigo](https://github.com/gomodule/redigo).

here is benchmark results compare to [redigo](https://github.com/gomodule/redigo) and [go-redis](https://github.com/go-redis/redis) with go1.10.1, i7-7700:

```
BenchmarkCmdSetRedisgo 	10000000	       188 ns/op	       5 B/op	       0 allocs/op
BenchmarkCmdSetRedigo  	 5000000	       329 ns/op	     112 B/op	       5 allocs/op
BenchmarkCmdSetGoredis 	 2000000	       748 ns/op	     372 B/op	       9 allocs/op
```

### Usage

```go
package main

import (
    "context"
    "log"
    "net"

    "github.com/xiaost/redisgo"
)

func main() {
    pool := redisgo.NewPool(func(ctx context.Context) (*redisgo.Conn, error) {
        var d net.Dialer
        conn, err := d.DialContext(ctx, "tcp", "127.0.0.1:6379")
        if err != nil {
            return nil, err
        }
        return redisgo.NewConn(conn), nil
    })
    defer pool.Close()

    ctx := context.TODO()

    // the Pool.DoXxx helpers borrow a conn for a single command
    if err := pool.DoNoReply(ctx, "SET", "hello", "world"); err != nil {
        log.Fatal(err)
    }
    s, err := pool.DoString(ctx, "GET", "hello")
    if err != nil { // err == redisgo.ErrNil if not exists
        log.Fatal(err)
    }
    if s != "world" {
        log.Fatal(s)
    }

    // borrow a conn for several commands, pipelining for example
    c, err := pool.Get(ctx)
    if err != nil {
        log.Fatal(err)
    }
    defer c.Close() // returns to pool

    c.Send("INCR", "n")
    c.Send("EXPIRE", "n", 60)
    reply := redisgo.NewReply()
    defer reply.Free()
    for range 2 {
        if err := c.Recv(reply); err != nil {
            log.Fatal(err)
        }
        if _, err := reply.Integer(); err != nil {
            log.Fatal(err)
        }
    }
}
```

### Notes

- `Conn` is not safe for concurrent use; borrow one `Conn` per goroutine from a `Pool`,
  or use the `Pool.DoXxx` helpers, which are safe for concurrent use.
- The `ctx` of `Pool.DoXxx` bounds getting a conn and, through its deadline, the command itself.
  Canceling a `ctx` without a deadline does not interrupt a command already sent.
- Replies are zero-copy: byte slices returned by `Reply.Bytes`, `DoBytes`, `DoBytesSlice` and `RedisErr`
  alias the read buffer of the connection.
  They stay valid after `Reply.Free` and after the conn is reused,
  but keeping one alive keeps the whole buffer alive,
  so copy values that you retain for long.
- `DoString`, `DoInteger` and `DoNoReply` return nothing that aliases the read buffer (except a `RedisErr`),
  so the connection reuses the buffer for replies up to 64KB, and they allocate nothing but the returned string.
  Prefer them unless you need zero-copy bytes.
- Call `Reply.Free` exactly once per `Reply` returned by `Do`.
- `PoolConn.Close` detaches the conn: a `PoolConn` used after `Close` returns `ErrClosed`
  instead of touching a conn that someone else may have borrowed since.
- A nil bulk string (`GET` of a missing key) and a nil array (`BLPOP` on timeout, an aborted `EXEC`)
  are the same: `Reply.IsNil` reports both and the getters and helpers return `ErrNil`.
- `Do` and the `DoXxx` helpers first receive and drop the replies still pending from `Send`,
  like redigo, so `Send("MULTI")`, `Send(...)`, `Do("EXEC")` returns the reply of `EXEC`.
  If a dropped reply is an error, the first one is returned instead.
- Bulk strings and arrays longer than 512MB (the default `proto-max-bulk-len` of redis) are rejected as a protocol error.
