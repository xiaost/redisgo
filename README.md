# redisgo

[![GoDoc](https://godoc.org/github.com/xiaost/redisgo?status.svg)](https://godoc.org/github.com/xiaost/redisgo)

A high-performance, zero-dependency Redis client for Go, inspired by [redigo](https://github.com/gomodule/redigo).

```
go get github.com/xiaost/redisgo
```

Benchmark vs [redigo](https://github.com/gomodule/redigo) and [go-redis](https://github.com/go-redis/redis) (go1.10.1, i7-7700):

```
BenchmarkCmdSetRedisgo 	10000000	       188 ns/op	       5 B/op	       0 allocs/op
BenchmarkCmdSetRedigo  	 5000000	       329 ns/op	     112 B/op	       5 allocs/op
BenchmarkCmdSetGoredis 	 2000000	       748 ns/op	     372 B/op	       9 allocs/op
```

## Quick Start

```go
package main

import (
    "context"
    "errors"
    "log"
    "time"

    "github.com/xiaost/redisgo"
)

func main() {
    pool := redisgo.NewPool(
        redisgo.Dialer("127.0.0.1:6379",
            redisgo.WithPassword("secret"), // optional
            redisgo.WithDB(0),
            redisgo.WithTimeout(5*time.Second),
        ),
        redisgo.WithMaxIdle(10),
        redisgo.WithMaxActive(100),
    )
    defer pool.Close()

    ctx := context.Background()

    if err := pool.DoNoReply(ctx, "SET", "greeting", "hello"); err != nil {
        log.Fatal(err)
    }

    val, err := pool.DoString(ctx, "GET", "greeting")
    if errors.Is(err, redisgo.ErrNil) {
        log.Println("key not found")
    } else if err != nil {
        log.Fatal(err)
    } else {
        log.Println("greeting:", val)
    }
}
```

Or connect from a URL (`redis://`, `rediss://`, `unix://`):

```go
dial, err := redisgo.DialerURL("redis://user:pass@127.0.0.1:6379/1",
    redisgo.WithTimeout(5*time.Second),
)
if err != nil {
    log.Fatal(err)
}
pool := redisgo.NewPool(dial)
defer pool.Close()
```

For a single standalone connection (not safe for concurrent use), call `redisgo.Dial(ctx, addr, opts...)` or `redisgo.DialURL(ctx, url, opts...)`.

## Common Commands

### Locks, Counters, Floats & Flags

```go
// SET NX -> true on "+OK", false on nil
locked, err := pool.DoOK(ctx, "SET", "lock:order:42", "owner", "NX", "EX", 30)

// DoInt / DoInt64 / DoUint64 / DoFloat64 parse both RESP integers and bulk strings
n, err := pool.DoInt64(ctx, "INCR", "hits")
cached, err := pool.DoInt64(ctx, "GET", "hits")         // parses "$3\r\n123\r\n" with 0 allocs
score, err := pool.DoFloat64(ctx, "ZSCORE", "rank", "u1")
exists, err := pool.DoBool(ctx, "EXISTS", "greeting")   // non-zero int, "1"/"0", or "true"/"false"
```

### Batch Arguments, Lists, Sets & Hashes

Slices (`[]string`, `[][]byte`, `[]int`, `[]int64`, `[]uint64`, `[]float64`, `[]any`) and maps (`map[string]string`, `map[string][]byte`, `map[string]any`) are flattened automatically without extra allocations:

```go
// Batch DEL / SADD with []int64 or []string
added, err := pool.DoInt64(ctx, "SADD", "online:users", []int64{101, 102, 103})
members, err := pool.DoStrings(ctx, "SMEMBERS", "online:users")
hits, err := pool.DoBools(ctx, "SMISMEMBER", "online:users", 101, 999)

// HSET / MSET with maps
err = pool.DoNoReply(ctx, "HSET", "user:1", map[string]any{
    "name": "alice",
    "age":  30,
})
user, err := pool.DoStringMap(ctx, "HGETALL", "user:1")
```

### Unpacking Mixed Replies with `Reply.Scan`

Use `Reply.Scan` for heterogeneous arrays like `MGET`, `HMGET`, or `EXEC`:

```go
r, err := pool.Do(ctx, "HMGET", "user:1", "name", "age", "missing")
if err != nil {
    log.Fatal(err)
}
defer r.Free()

var (
    name    string
    age     int
    missing string // nil elements reset to zero value
)
if err := r.Scan(&name, &age, &missing); err != nil {
    log.Fatal(err)
}
```

Pass `nil` to skip an element (`r.Scan(&name, nil, &missing)`). Byte slices scanned into `*[]byte` or `*[][]byte` are copied and safe to retain.

## Pipelining & Transactions

### Pipelining

`Pool.Pipeline` borrows a connection, runs your callback with sticky error handling, automatically flushes and drains any unread replies when the callback returns `nil`, and returns the connection to the pool:

```go
// Read replies with typed RecvXxx (reuses read buffer)
var count int64
var ok bool
err := pool.Pipeline(ctx, func(p *redisgo.Pipeline) error {
    p.Send("SET", "k", "v")
    p.Send("INCR", "n")
    p.Send("EXPIRE", "n", 60)

    if err := p.RecvNoReply(); err != nil {
        return err
    }
    var err error
    if count, err = p.RecvInt64(); err != nil {
        return err
    }
    ok, err = p.RecvBool()
    return err
})
```

Fire-and-forget pipelines only need `Send` — unread replies are drained on return, surfacing the first `RedisErr` if any:

```go
err := pool.Pipeline(ctx, func(p *redisgo.Pipeline) error {
    p.Send("SET", "a", 1)
    p.Send("SET", "b", 2)
    p.Send("EXPIRE", "a", 60)
    return nil
})
```

### Transactions (`MULTI` / `EXEC`)

Use `Pool.WithConn` when you need a single connection across multiple calls (no `PoolConn` wrapper allocation):

```go
err := pool.WithConn(ctx, func(c *redisgo.Conn) error {
    c.Send("MULTI")
    c.Send("DECRBY", "balance:a", 50)
    c.Send("INCRBY", "balance:b", 50)
    r, err := c.Do("EXEC") // drains QUEUED replies
    if err != nil {
        return err
    }
    defer r.Free()

    var balA, balB int64
    return r.Scan(&balA, &balB) // returns ErrNil if WATCH aborted EXEC
})
```

## Scanning Keys

Iterate keys cleanly with Go range-over-func; `Pool.Scan` releases its connection between pages so you can issue other `pool.DoXxx` commands inside the loop without deadlocking:

```go
for key, err := range pool.Scan(ctx, "MATCH", "session:*", "COUNT", 200) {
    if err != nil {
        log.Fatal(err)
    }
    ttl, err := pool.DoInt64(ctx, "TTL", key)
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("%s ttl=%d", key, ttl)
}
```

For `HSCAN`, `SSCAN`, and `ZSCAN`, use `DoScan`:

```go
for cursor := uint64(0); ; {
    next, items, err := pool.DoScan(ctx, "HSCAN", "user:1", cursor, "COUNT", 100)
    if err != nil {
        log.Fatal(err)
    }
    _ = items
    if cursor = next; cursor == 0 {
        break
    }
}
```

## Lua Scripts

`NewScript` caches the SHA1 digest and runs `EVALSHA`, transparently falling back to `EVAL` if Redis responds with `NOSCRIPT`:

```go
var unlockScript = redisgo.NewScript(1, `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0`)

released, err := unlockScript.DoBool(ctx, pool, "lock:order:42", "owner")
```

## Pub/Sub

Commands where Redis pushes unsolicited replies (`SUBSCRIBE`, `PSUBSCRIBE`, `SSUBSCRIBE`, `MONITOR`) must use `Pool.Get` with `Send` and `Recv` (the pool closes push-mode connections instead of reusing them):

```go
c, err := pool.Get(ctx)
if err != nil {
    log.Fatal(err)
}
defer c.Close()
c.SetReadTimeout(0) // wait indefinitely for messages

if err := c.Send("SUBSCRIBE", "events"); err != nil {
    log.Fatal(err)
}
r := redisgo.NewReply()
defer r.Free()
for {
    if err := c.Recv(r); err != nil {
        log.Fatal(err)
    }
    aa, err := r.Array()
    if err != nil {
        log.Fatal(err)
    }
    _ = aa
}
```

## Performance & Zero-Copy Guide

| API | Read Buffer | Notes |
| --- | --- | --- |
| `DoNoReply`, `DoOK`, `DoInteger`, `DoInt`, `DoInt64`, `DoUint64`, `DoFloat64`, `DoBool` | **Reused** (up to 64KB) | **0 allocs** on valid replies; safe to reuse conn immediately |
| `DoString`, `DoStrings`, `DoInt64s`, `DoFloat64s`, `DoBools`, `DoStringMap`, `DoInt64Map`, `DoScan` | **Reused** (up to 64KB) | Copies/parses result; `Strings` / maps pack small string payloads (`<= 4KB`) into 1 buffer |
| `RecvNoReply`, `RecvOK`, `RecvString`, `RecvInt64`, `RecvStrings`, `RecvStringMap`, ... (`Conn` & `Pipeline`) | **Reused** (up to 64KB) | Same as `DoXxx` for pipelines without `Reply` pool round-trips |
| `DoBytes`, `DoBytesSlice`, `RecvBytes`, `RecvBytesSlice` | **Exposed (pinned)** | **Zero-copy** slice aliasing the read buffer; copy before long-term retention |
| `Do` + `Reply.Free`, `Send` + `Recv` | **Exposed (pinned)** | Full control over raw RESP replies; always call `Reply.Free()` once |

## Error Handling & Notes

- **Nil replies**: Missing keys (`GET`) and nil arrays (`BLPOP` timeout, aborted `EXEC`) return `ErrNil` (`Reply.IsNil() == true`). Only `DoOK` / `RecvOK` return `(false, nil)` for nil.
- **Server errors**: Redis `-ERR` replies return `RedisErr`, which aliases the read buffer (copy `err.Error()` if retaining long-term). Check prefixes with zero allocations:
  ```go
  var re redisgo.RedisErr
  if errors.As(err, &re) && re.HasPrefix("WRONGTYPE") {
      // handle wrong key type
  }
  ```
- **Context deadlines**: The `ctx` passed to `Pool.DoXxx`, `Pool.Pipeline`, `Pool.WithConn`, and `Script.DoXxx` bounds waiting for a conn and caps every read/write via `ctx.Deadline()`. Canceling a `ctx` without a deadline does not interrupt a command already sent.
- **Session isolation**: `Pool` automatically closes connections whose session state was altered (`SELECT`, `AUTH`, `HELLO`, `RESET`, `CLIENT SETNAME`, `SUBSCRIBE`, `MONITOR`, or an unfinished `MULTI` / `WATCH`), so borrowed connections always start in the base state set by `Dialer`.
- **Stale `PoolConn` safety**: `PoolConn.Close()` is idempotent and detaches the underlying `Conn`; subsequent calls on that `PoolConn` return `ErrClosed`.
- **Protocol limits**: Bulk strings and arrays larger than 512MB (`proto-max-bulk-len`) and array nesting deeper than 64 levels are rejected with `ErrProtocol`.
