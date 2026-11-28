package redisgo

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Dial connects to Redis at addr and runs any configured handshake commands
// (AUTH, SELECT, CLIENT SETNAME) pipelined in a single round-trip.
func Dial(ctx context.Context, addr string, ops ...Option) (*Conn, error) {
	return dialWithOptions(ctx, addr, applyOptions(ops))
}

// Dialer returns a DialFunc for NewPool that connects to addr with ops.
func Dialer(addr string, ops ...Option) DialFunc {
	o := applyOptions(ops)
	if o.tlsConfig != nil {
		o.tlsConfig = o.tlsConfig.Clone()
	}
	return func(ctx context.Context) (*Conn, error) {
		return dialWithOptions(ctx, addr, o)
	}
}

// DialURL parses a Redis URL ("redis://", "rediss://", or "unix://") and
// connects to it. Explicit ops override settings parsed from the URL.
func DialURL(ctx context.Context, rawurl string, ops ...Option) (*Conn, error) {
	addr, o, err := parseDialURL(rawurl, ops...)
	if err != nil {
		return nil, err
	}
	return dialWithOptions(ctx, addr, o)
}

// DialerURL parses a Redis URL ("redis://", "rediss://", or "unix://") and
// returns a DialFunc for NewPool. Explicit ops override settings parsed from
// the URL.
func DialerURL(rawurl string, ops ...Option) (DialFunc, error) {
	addr, o, err := parseDialURL(rawurl, ops...)
	if err != nil {
		return nil, err
	}
	if o.tlsConfig != nil {
		o.tlsConfig = o.tlsConfig.Clone()
	}
	return func(ctx context.Context) (*Conn, error) {
		return dialWithOptions(ctx, addr, o)
	}, nil
}

func parseDialURL(rawurl string, ops ...Option) (string, options, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", options{}, err
	}
	o := defaultoptions
	var addr string
	switch u.Scheme {
	case "redis", "rediss":
		host := u.Hostname()
		if host == "" {
			return "", options{}, errors.New("redisgo: missing host in URL")
		}
		port := u.Port()
		if port == "" {
			port = "6379"
		}
		addr = net.JoinHostPort(host, port)
		if u.Scheme == "rediss" {
			o.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		if u.Path != "" && u.Path != "/" {
			p := strings.TrimPrefix(u.Path, "/")
			db, err := parseNonNegInt(p)
			if err != nil {
				return "", options{}, fmt.Errorf("redisgo: invalid database %q in URL", p)
			}
			o.db = db
		}
	case "unix":
		if u.Path == "" {
			return "", options{}, errors.New("redisgo: missing socket path in URL")
		}
		o.network = "unix"
		addr = u.Path
		if rawDB := u.Query().Get("db"); rawDB != "" {
			db, err := parseNonNegInt(rawDB)
			if err != nil {
				return "", options{}, fmt.Errorf("redisgo: invalid database %q in URL", rawDB)
			}
			o.db = db
		}
	default:
		return "", options{}, fmt.Errorf("redisgo: unsupported URL scheme %q", u.Scheme)
	}
	if u.User != nil {
		o.username = u.User.Username()
		if pw, ok := u.User.Password(); ok {
			o.password = pw
		}
	}
	if q := u.Query(); u.Scheme != "unix" && q.Has("db") {
		if u.Path != "" && u.Path != "/" {
			return "", options{}, errors.New("redisgo: database specified in both path and query")
		}
		rawDB := q.Get("db")
		db, err := parseNonNegInt(rawDB)
		if err != nil {
			return "", options{}, fmt.Errorf("redisgo: invalid database %q in URL", rawDB)
		}
		o.db = db
	}
	for _, op := range ops {
		o = op(o)
	}
	return addr, o, nil
}

func parseNonNegInt(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty integer")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("invalid digit")
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, errors.New("out of range")
	}
	return n, nil
}

func dialWithOptions(ctx context.Context, addr string, o options) (*Conn, error) {
	if o.network == "" {
		o.network = "tcp"
	}
	d := net.Dialer{Timeout: o.dialTimeout}
	var (
		nc  net.Conn
		err error
	)
	if o.tlsConfig != nil {
		td := tls.Dialer{NetDialer: &d, Config: o.tlsConfig}
		nc, err = td.DialContext(ctx, o.network, addr)
	} else {
		nc, err = d.DialContext(ctx, o.network, addr)
	}
	if err != nil {
		return nil, err
	}
	return initConn(ctx, nc, o)
}

func initConn(ctx context.Context, nc net.Conn, o options) (*Conn, error) {
	c := newConn(nc, o)
	if err := ctx.Err(); err != nil {
		c.Close()
		return nil, err
	}
	needAuth := o.username != "" || o.password != ""
	needDB := o.db != 0
	needName := o.clientName != ""
	if !needAuth && !needDB && !needName {
		return c, nil
	}
	var limit time.Time
	var byCtx bool
	if d, ok := ctx.Deadline(); ok {
		limit = d
		byCtx = true
	}
	if o.dialTimeout > 0 {
		dl := time.Now().Add(o.dialTimeout)
		if !byCtx || dl.Before(limit) {
			limit = dl
			byCtx = false
		}
	}
	if !limit.IsZero() {
		c.setLimit(limit)
	}
	count := 0
	var err error
	if needAuth {
		count++
		if o.username != "" {
			err = c.Send("AUTH", o.username, o.password)
		} else {
			err = c.Send("AUTH", o.password)
		}
	}
	if err == nil && needDB {
		count++
		err = c.Send("SELECT", o.db)
	}
	if err == nil && needName {
		count++
		err = c.Send("CLIENT", "SETNAME", o.clientName)
	}
	if err == nil {
		for range count {
			if err = c.RecvNoReply(); err != nil {
				break
			}
		}
	}
	if err != nil {
		if byCtx && c.br.hasLimit && c.timedOutByLimit(err) {
			err = context.DeadlineExceeded
		}
		c.setLimit(time.Time{})
		c.Close()
		return nil, err
	}
	c.setLimit(time.Time{})
	return c, nil
}
