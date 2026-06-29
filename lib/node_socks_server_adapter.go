package almagest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/socksgo"
	"github.com/asciimoth/socksgo/protocol"
	"github.com/coder/websocket"
)

func buildSocksGoServer(logger pasta.Logger, cfg socksServerWorkerConfig) *socksgo.Server {
	listen := cfg.listenNetwork
	proxy := cfg.proxyNetwork
	server := &socksgo.Server{
		Pool:           cfg.pool,
		Auth:           socksServerAuth(cfg.config.users),
		Handlers:       socksServerHandlers(cfg.config.commands),
		Dialer:         proxy.Dial,
		PacketDialer:   proxy.PacketDial,
		Listener:       proxy.Listen,
		PacketListener: proxy.ListenPacket,
		AssocListener: func(ctx context.Context, ctrl net.Conn) (gonnect.PacketConn, error) {
			return listenSocksUDPAssoc(ctx, listen, ctrl)
		},
		Resolver: proxy,
		PreCmd: func(_ context.Context, conn net.Conn, ver string, info protocol.AuthInfo, cmd protocol.Cmd, addr protocol.Addr) (protocol.ReplyStatus, error) {
			logger.Debugf("socks %s %s %s auth=%s", ver, cmd.String(), addr.String(), socksAuthSummary(info))
			return 0, nil
		},
		HandshakeTimeout: 10 * time.Second,
	}
	return server
}

func listenSocksUDPAssoc(ctx context.Context, network gonnect.Network, ctrl net.Conn) (gonnect.PacketConn, error) {
	la := ctrl.LocalAddr()
	if la == nil {
		return nil, errors.New("failed to guess default local addr for incoming UDP assoc connections")
	}
	host := la.String()
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return network.ListenPacket(ctx, "udp", net.JoinHostPort(host, "0"))
}

func serveSocksListener(ctx context.Context, logger pasta.Logger, server *socksgo.Server, ln net.Listener, errs chan<- error) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				errs <- err
			}
			return
		}
		logger.Debugf("socks client connected from %s", conn.RemoteAddr())
		go func() {
			err := server.Accept(ctx, conn, false)
			if err != nil {
				logger.Debugf("socks client %s closed: %v", conn.RemoteAddr(), err)
			} else {
				logger.Debugf("socks client %s closed", conn.RemoteAddr())
			}
		}()
	}
}

func socksWebSocketHandler(ctx context.Context, logger pasta.Logger, server *socksgo.Server, path string) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(rw, r)
			return
		}
		conn, err := websocket.Accept(rw, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			logger.Debugf("socks+ws upgrade failed from %s: %v", r.RemoteAddr, err)
			return
		}
		defer func() { _ = conn.Close(websocket.StatusInternalError, "") }()
		logger.Debugf("socks+ws client connected from %s", r.RemoteAddr)
		if err := server.AcceptWS(ctx, conn, r.TLS != nil); err != nil {
			logger.Debugf("socks+ws client %s closed: %v", r.RemoteAddr, err)
		} else {
			logger.Debugf("socks+ws client %s closed", r.RemoteAddr)
		}
	})
}

func socksServerAuth(users []socksServerUser) *protocol.AuthHandlers {
	if len(users) == 0 {
		return nil
	}
	allowed := make(map[string]map[string]struct{}, len(users))
	for _, user := range users {
		if allowed[user.username] == nil {
			allowed[user.username] = map[string]struct{}{}
		}
		allowed[user.username][user.password] = struct{}{}
	}
	return (&protocol.AuthHandlers{}).Add(&protocol.PassAuthHandler{VerifyFn: func(user, pass string) bool {
		passwords, ok := allowed[user]
		if !ok {
			return false
		}
		_, ok = passwords[pass]
		return ok
	}})
}

func socksServerHandlers(enabled map[protocol.Cmd]bool) map[protocol.Cmd]socksgo.CommandHandler {
	handlers := make(map[protocol.Cmd]socksgo.CommandHandler)
	for cmd, handler := range socksgo.DefaultCommandHandlers {
		if enabled[cmd] {
			handlers[cmd] = handler
		}
	}
	return handlers
}
