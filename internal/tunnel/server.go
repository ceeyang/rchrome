package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"
)

// Logf is a printf-style logger.
type Logf func(format string, args ...any)

// ServerOptions configures StartServer.
type ServerOptions struct {
	KeyPath   string // persistent identity; ignored when Ephemeral
	Ephemeral bool   // one-shot address, key discarded on exit
	Port      uint16 // the only port clients may connect to
	Allow     []string
	Handler   func(conn net.Conn, peer string)
	Logf      Logf
}

// Server is a running tailcat server exposing exactly one port.
type Server struct {
	s          *tailcat.Server
	Addr       string
	RegionName string
	Created    bool
}

// StartServer brings up the tunnel server and returns its tc… address.
func StartServer(ctx context.Context, o ServerOptions) (*Server, error) {
	if o.Handler == nil {
		return nil, errors.New("tunnel: Handler is required")
	}
	logf := o.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	var (
		conf    *tailcat.PrivateKey
		created bool
		err     error
	)
	if o.Ephemeral {
		conf = tailcat.NewPrivateKey()
		conf.Public.RegionID = -1
	} else {
		conf, created, err = LoadOrCreateServerKey(o.KeyPath)
		if err != nil {
			return nil, err
		}
	}
	priv := conf.Private
	psk := conf.Public.PresharedKey
	if psk.IsZero() {
		return nil, fmt.Errorf("密钥文件缺少预共享密钥，请删除 %s 后重试 / key file has no PSK", o.KeyPath)
	}

	// Resolve the relay region: probe once (RegionID -1), then pin it.
	ci := conf.Public
	ci.Region = nil
	if err := ci.Expand(ctx, tailcat.ExpandForServer); err != nil {
		return nil, fmt.Errorf("选择中继失败（网络不通？）/ relay selection failed: %w", err)
	}
	if len(ci.Region) == 0 {
		return nil, errors.New("tunnel: no relay region available")
	}
	reg := ci.Region[0]
	if !o.Ephemeral && conf.Public.RegionID <= 0 {
		conf.Public.RegionID = reg.RegionID
		if err := SaveServerKey(o.KeyPath, conf); err != nil {
			logf("warning: could not pin relay region into %s: %v", o.KeyPath, err)
		}
	}

	addr := (&tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: priv.Public()},
		ServerDiscoPublic: tailcat.DiscoPublicForNode(priv),
		PresharedKey:      psk,
		RegionID:          reg.RegionID,
	}).Addr()

	s := &tailcat.Server{
		Key:            priv,
		PresharedKey:   psk,
		Region:         reg,
		Logf:           func(f string, a ...any) { logf(f, a...) },
		ServedTCPPorts: []filter.PortRange{{First: o.Port, Last: o.Port}},
	}
	if len(o.Allow) > 0 {
		var ks tailcat.KeySet
		for _, raw := range o.Allow {
			for _, item := range strings.Split(raw, ",") {
				item = strings.TrimSpace(item)
				if item == "" {
					continue
				}
				var k key.NodePublic
				if err := k.UnmarshalText([]byte(item)); err != nil {
					return nil, fmt.Errorf("--allow %q 不是合法的 nodekey / invalid node key: %w", item, err)
				}
				ks.Add(k)
			}
		}
		s.AllowClient = ks.Contains
	}
	s.OnTCP = func(port uint16) func(net.Conn) {
		if port != o.Port {
			return nil
		}
		return func(c net.Conn) {
			peer := ""
			if k, ok := s.PeerKey(c.RemoteAddr()); ok {
				peer = k.String()
			}
			o.Handler(c, peer)
		}
	}
	if err := s.StartContext(ctx); err != nil {
		return nil, fmt.Errorf("隧道启动失败 / tunnel start failed: %w", err)
	}
	return &Server{s: s, Addr: string(addr), RegionName: reg.RegionName, Created: created}, nil
}

// Close shuts the tunnel down.
func (s *Server) Close() error { return s.s.Close() }
