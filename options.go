package redisgo

import (
	"crypto/tls"
	"time"
)

type Option func(opt options) options

type options struct {
	rbuf        int
	wbuf        int
	rtimeout    time.Duration
	wtimeout    time.Duration
	network     string
	dialTimeout time.Duration
	username    string
	password    string
	db          int
	clientName  string
	tlsConfig   *tls.Config
}

var defaultoptions = options{
	rbuf:        2048,
	wbuf:        2048,
	rtimeout:    30 * time.Second,
	wtimeout:    30 * time.Second,
	network:     "tcp",
	dialTimeout: 5 * time.Second,
}

func applyOptions(ops []Option) options {
	o := defaultoptions
	for _, op := range ops {
		o = op(o)
	}
	return o
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

// WithTimeout sets both the read and write timeouts of the connection.
func WithTimeout(t time.Duration) Option {
	return func(opt options) options {
		opt.rtimeout = t
		opt.wtimeout = t
		return opt
	}
}

// WithNetwork sets the network passed to net.Dialer (default "tcp").
func WithNetwork(network string) Option {
	return func(opt options) options {
		opt.network = network
		return opt
	}
}

// WithDialTimeout sets the timeout for dialing a connection (default 5s).
func WithDialTimeout(t time.Duration) Option {
	return func(opt options) options {
		opt.dialTimeout = t
		return opt
	}
}

// WithUsername sets the ACL username used by Dial and Dialer.
func WithUsername(username string) Option {
	return func(opt options) options {
		opt.username = username
		return opt
	}
}

// WithPassword sets the password used by Dial and Dialer.
func WithPassword(password string) Option {
	return func(opt options) options {
		opt.password = password
		return opt
	}
}

// WithDB sets the Redis database selected by Dial and Dialer.
func WithDB(db int) Option {
	return func(opt options) options {
		opt.db = db
		return opt
	}
}

// WithClientName sets the CLIENT SETNAME executed by Dial and Dialer.
func WithClientName(name string) Option {
	return func(opt options) options {
		opt.clientName = name
		return opt
	}
}

// WithTLSConfig enables TLS with cfg in Dial and Dialer.
func WithTLSConfig(cfg *tls.Config) Option {
	return func(opt options) options {
		opt.tlsConfig = cfg
		return opt
	}
}
