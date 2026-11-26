package tunnel

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/tailscale/tailcat"
)

// Client dials ports on a remote rchrome serve.
type Client struct {
	cl *tailcat.Client
}

// NewClient prepares a client for addr. keyPath, if non-empty, loads or creates
// a persistent identity so the server can allow-list this machine. Nothing
// touches the network until the first Dial.
func NewClient(addr string, keyPath string, logf Logf) (*Client, error) {
	if _, err := tailcat.ParseAddr(tailcat.Addr(addr)); err != nil {
		return nil, fmt.Errorf("地址不合法，请完整复制 tc… 开头的那一串 / invalid address: %w", err)
	}
	cl := tailcat.NewClient(tailcat.Addr(addr))
	if logf != nil {
		cl.Logf = func(f string, a ...any) { logf(f, a...) }
	} else {
		cl.Logf = func(string, ...any) {}
	}
	if keyPath != "" {
		if k, err := LoadOrCreateClientKey(keyPath); err == nil {
			cl.Key = k
		}
	}
	return &Client{cl: cl}, nil
}

// Dial opens a TCP connection to port on the remote machine.
func (c *Client) Dial(ctx context.Context, port uint16) (net.Conn, error) {
	return c.cl.DialTCPPort(ctx, port)
}

// PublicKey is this client's node key, for `serve --allow`.
func (c *Client) PublicKey() string { return c.cl.PublicKey().String() }

// Ping measures the relay round trip.
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	r, err := c.cl.Ping(ctx)
	if err != nil {
		return 0, err
	}
	return r.Latency, nil
}

// Close tears the client down.
func (c *Client) Close() error { return c.cl.Close() }

// Forward accepts on ln and relays every connection to remotePort. When the
// remote dial fails, onDialError (if set) takes ownership of the local conn so
// it can answer with a readable error instead of a bare reset.
func (c *Client) Forward(ctx context.Context, ln net.Listener, remotePort uint16, onDialError func(net.Conn, error)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			dctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			remote, err := c.cl.DialTCPPort(dctx, remotePort)
			cancel()
			if err != nil {
				if onDialError != nil {
					onDialError(conn, err)
				} else {
					_ = conn.Close()
				}
				return
			}
			Pipe(conn, remote)
		}()
	}
}
