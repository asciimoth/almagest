package socksnodes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
	"github.com/shadowsocks/go-shadowsocks2/socks"
	ss "golang.getoutline.org/sdk/transport/shadowsocks"
)

func runShadowsocksServer(ctx context.Context, w *pasta.Workspace, node, generation uint64, stop <-chan struct{}, cfg shadowsocksConfig, listen, proxy gonnect.Network, spawner WorkerSpawner, replay *shadowsocksReplayGuard) {
	if cfg.password == "" {
		sendShadowsocksStatus(w, node, generation, false, errors.New("shadowsocks password is unavailable"))
		return
	}
	key, err := ss.NewEncryptionKey(cfg.method, cfg.password)
	if err != nil {
		sendShadowsocksStatus(w, node, generation, false, err)
		return
	}
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		default:
		}
		err := runShadowsocksServerOnce(ctx, w, node, generation, stop, cfg, key, listen, proxy, spawner, replay)
		if err != nil {
			sendShadowsocksStatus(w, node, generation, false, err)
		}
		if !sleepOrStopped(stop, shadowsocksServerRetryDelay) {
			return
		}
	}
}
func sendShadowsocksStatus(w *pasta.Workspace, node, generation uint64, ready bool, err error) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: shadowsocksServerStatus{generation: generation, ready: ready, err: err}})
}
func runShadowsocksServerOnce(ctx context.Context, w *pasta.Workspace, node, generation uint64, stop <-chan struct{}, cfg shadowsocksConfig, key *ss.EncryptionKey, listen, proxy gonnect.Network, spawner WorkerSpawner, replay *shadowsocksReplayGuard) error {
	listenNet := gonnect.DetachNetwork(listen, nil, nil)
	proxyNet := gonnect.DetachNetwork(proxy, nil, nil)
	var workers sync.WaitGroup
	defer func() { _ = listenNet.Close(); _ = proxyNet.Close(); workers.Wait() }()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	address := cfg.listenAddress()
	if cfg.tcp {
		listener, err := listenNet.Listen(runCtx, "tcp", address)
		if err != nil {
			return fmt.Errorf("TCP listen: %w", err)
		}
		if _, err := spawner.SpawnWg(func() { serveShadowsocksTCP(runCtx, listener, proxyNet, key, spawner, &workers, errs, replay) }, &workers, "Shadowsocks TCP listener"); err != nil {
			return err
		}
	}
	if cfg.udp {
		packet, err := listenNet.ListenPacket(runCtx, "udp", address)
		if err != nil {
			return fmt.Errorf("UDP listen: %w", err)
		}
		if _, err := spawner.SpawnWg(func() { serveShadowsocksUDP(runCtx, packet, proxyNet, key, spawner, &workers, errs, replay) }, &workers, "Shadowsocks UDP listener"); err != nil {
			return err
		}
	}
	sendShadowsocksStatus(w, node, generation, true, nil)
	select {
	case <-stop:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errs:
		return err
	}
}
func serveShadowsocksTCP(ctx context.Context, listener net.Listener, proxy gonnect.Network, key *ss.EncryptionKey, spawner WorkerSpawner, workers *sync.WaitGroup, errs chan<- error, replay *shadowsocksReplayGuard) {
	slots := make(chan struct{}, shadowsocksTCPMaxConnections)
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				errs <- err
			}
			return
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		if _, err := spawner.SpawnWg(func() {
			defer func() { <-slots }()
			relayShadowsocksTCP(ctx, conn, proxy, key, replay)
		}, workers, "Shadowsocks TCP relay"); err != nil {
			<-slots
			_ = conn.Close()
			errs <- err
			return
		}
	}
}

const (
	shadowsocksTCPMaxConnections = 1024
	shadowsocksTCPHandshakeTime  = 10 * time.Second
	shadowsocksTCPDialTimeout    = 10 * time.Second
)

func relayShadowsocksTCP(ctx context.Context, client net.Conn, proxy gonnect.Network, key *ss.EncryptionKey, replay *shadowsocksReplayGuard) {
	relayShadowsocksTCPWithTimeout(ctx, client, proxy, key, replay, shadowsocksTCPHandshakeTime, shadowsocksTCPDialTimeout)
}

func relayShadowsocksTCPWithTimeout(ctx context.Context, client net.Conn, proxy gonnect.Network, key *ss.EncryptionKey, replay *shadowsocksReplayGuard, handshakeTime, dialTimeout time.Duration) {
	defer client.Close() //nolint:errcheck
	// Close enforces the handshake timeout even when a supplied Network does
	// not support deadlines on accepted connections.
	handshake := time.AfterFunc(handshakeTime, func() { _ = client.Close() })
	defer handshake.Stop()
	_ = client.SetReadDeadline(time.Now().Add(handshakeTime))
	salt := make([]byte, key.SaltSize())
	if _, err := io.ReadFull(client, salt); err != nil {
		return
	}
	reader := ss.NewReader(io.MultiReader(bytes.NewReader(salt), client), key)
	target, err := socks.ReadAddr(reader)
	if err != nil {
		return
	}
	if replay != nil && !replay.accept(salt) {
		return
	}
	if !handshake.Stop() {
		return
	}
	_ = client.SetReadDeadline(time.Time{})
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	destination, err := proxy.Dial(dialCtx, "tcp", target.String())
	cancel()
	if err != nil {
		return
	}
	defer destination.Close() //nolint:errcheck
	writer := ss.NewWriter(client, key)
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(destination, reader)
		if half, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = destination.Close()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(writer, destination)
		if half, ok := client.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		} else {
			_ = client.Close()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}

type shadowsocksUDPSession struct {
	conn    gonnect.PacketConn
	target  string
	client  net.Addr
	packets chan []byte
}

type shadowsocksUDPSessionKey struct{ client, target string }

const (
	shadowsocksUDPMaxSessions = 1024
	shadowsocksUDPQueueSize   = 2
	shadowsocksUDPDialTimeout = 10 * time.Second
	shadowsocksUDPIdleTimeout = 60 * time.Second
)

func serveShadowsocksUDP(ctx context.Context, listener gonnect.PacketConn, proxy gonnect.Network, key *ss.EncryptionKey, spawner WorkerSpawner, workers *sync.WaitGroup, errs chan<- error, replay *shadowsocksReplayGuard) {
	sessions := make(map[shadowsocksUDPSessionKey]*shadowsocksUDPSession)
	var mu sync.Mutex
	buffer := make([]byte, 65535)
	for {
		size, client, err := listener.ReadFrom(buffer)
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				errs <- err
			}
			return
		}
		plaintext, err := ss.Unpack(nil, buffer[:size], key)
		if err != nil {
			continue
		}
		if replay != nil && !replay.accept(buffer[:key.SaltSize()]) {
			continue
		}
		target := socks.SplitAddr(plaintext)
		if target == nil {
			continue
		}
		data := append([]byte(nil), plaintext[len(target):]...)
		targetText := target.String()
		id := shadowsocksUDPSessionKey{client: client.String(), target: targetText}
		mu.Lock()
		session := sessions[id]
		if session == nil {
			if len(sessions) >= shadowsocksUDPMaxSessions {
				mu.Unlock()
				continue
			}
			session = &shadowsocksUDPSession{target: targetText, client: client, packets: make(chan []byte, shadowsocksUDPQueueSize)}
			sessions[id] = session
			current := session
			_, err = spawner.SpawnWg(func() {
				runShadowsocksUDPSession(ctx, listener, proxy, current, key)
				mu.Lock()
				if sessions[id] == current {
					delete(sessions, id)
				}
				mu.Unlock()
			}, workers, "Shadowsocks UDP relay")
			if err != nil {
				delete(sessions, id)
				mu.Unlock()
				errs <- err
				return
			}
		}
		mu.Unlock()
		select {
		case session.packets <- data:
		default: // UDP has no flow control; keep the receive loop responsive.
		}
	}
}

func runShadowsocksUDPSession(ctx context.Context, listener gonnect.PacketConn, proxy gonnect.Network, session *shadowsocksUDPSession, key *ss.EncryptionKey) {
	dialCtx, cancel := context.WithTimeout(ctx, shadowsocksUDPDialTimeout)
	conn, err := proxy.PacketDial(dialCtx, "udp", session.target)
	cancel()
	if err != nil {
		return
	}
	session.conn = conn
	done := make(chan struct{})
	go func() { relayShadowsocksUDPReply(listener, session, key); close(done) }()
	defer func() { _ = conn.Close(); <-done }()
	idle := time.NewTimer(shadowsocksUDPIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case data := <-session.packets:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(shadowsocksUDPIdleTimeout)
			if _, err := conn.Write(data); err != nil {
				return
			}
		case <-done:
			return
		case <-idle.C:
			return
		case <-ctx.Done():
			return
		}
	}
}
func relayShadowsocksUDPReply(listener gonnect.PacketConn, session *shadowsocksUDPSession, key *ss.EncryptionKey) {
	target := socks.ParseAddr(session.target)
	if target == nil {
		return
	}
	buffer := make([]byte, 65535)
	for {
		n, source, err := session.conn.ReadFrom(buffer)
		if errors.Is(err, gonnect.ErrUnsupported) {
			n, err = session.conn.Read(buffer)
			source = nil
		}
		if err != nil {
			return
		}
		responseAddr := target
		if source != nil {
			if parsed := socks.ParseAddr(source.String()); parsed != nil {
				responseAddr = parsed
			}
		}
		plaintext := make([]byte, len(responseAddr)+n)
		copy(plaintext, responseAddr)
		copy(plaintext[len(responseAddr):], buffer[:n])
		encrypted, err := ss.Pack(make([]byte, key.SaltSize()+len(plaintext)+key.TagSize()), plaintext, key)
		if err != nil {
			return
		}
		if _, err := listener.WriteTo(encrypted, session.client); err != nil {
			return
		}
	}
}
