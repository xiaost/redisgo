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
        conn, err := net.Dial("tcp", "127.0.0.1:6379")
        if err != nil {
            return nil, err
        }
        return redisgo.NewConn(conn), nil
    })

    redisconn, err := pool.Get(context.TODO())
    if err != nil {
        log.Fatal(err)
    }
    defer redisconn.Close() // returns to pool

    err = redisconn.DoNoReply("SET", "hello", "world")
    /*  or you can do it like this:
    reply, err := c.Do(cmd, args...)
    if err == nil {
        err = reply.Err()
        reply.Free()
    }
    */
    if err != nil {
        log.Fatal(err)
    }

    b, err := redisconn.DoBytes("GET", "hello")
    /* or you can do it like this:
    var b []byte
    reply, err := c.Do("GET", "hello")
    if err == nil {
        b, err = reply.Bytes()
        reply.Free()
    }
    */
    if err != nil { // err == redisgo.ErrNil if not exists
        log.Fatal(err)
    }
    if s := string(b); s != "world" {
        log.Fatal(s)
    }
}
```
