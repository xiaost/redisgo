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
