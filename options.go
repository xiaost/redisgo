package redisgo

import "time"

type Option func(opt options) options

type options struct {
	rbuf     int
	wbuf     int
	rtimeout time.Duration
	wtimeout time.Duration
}

var defaultoptions = options{
	rbuf:     2048,
	wbuf:     2048,
	rtimeout: 30 * time.Second,
	wtimeout: 30 * time.Second,
}

// WithReadBuffer set read buffer size of connection
func WithReadBuffer(sz int) Option {
	return func(opt options) options {
		opt.rbuf = sz
		return opt
	}
}

// WithWriteBuffer set write buffer size of connection
func WithWriteBuffer(sz int) Option {
	return func(opt options) options {
		opt.wbuf = sz
		return opt
	}
}

// WithReadTimeout set read timeout of Conn.Recv
func WithReadTimeout(t time.Duration) Option {
	return func(opt options) options {
		opt.rtimeout = t
		return opt
	}
}

// WithWriteTimeout set write timeout of Conn.Send
func WithWriteTimeout(t time.Duration) Option {
	return func(opt options) options {
		opt.wtimeout = t
		return opt
	}
}
