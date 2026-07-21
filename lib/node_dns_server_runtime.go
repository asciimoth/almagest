package almagest

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
)

var dnsServerNewServer = func(conn net.PacketConn, upstream dns.Interface, spawner gonnect.Spawner) dnsServerRuntime {
	return dns.NewServer(conn, upstream, spawner)
}

type dnsServerRuntime interface {
	dns.Interface
	Attach(dns.Interface)
	Detach()
}

type dnsServerWorker = restartableWorker

type dnsServerWorkerConfig struct {
	config   dnsServerConfig
	network  NetworkCloser
	upstream dns.Interface
	spawner  gonnect.Spawner
}

type dnsServerStart struct {
	id  uint64
	cfg dnsServerWorkerConfig
}

type dnsServerReady struct {
	id     uint64
	server dnsServerRuntime
	addr   string
}

type dnsServerRuntimeFailed struct {
	id  uint64
	err string
}

type dnsServerLog struct {
	level string
	text  string
}

func (n *dnsServerNode) restartServer() {
	n.restartID++
	id := n.restartID
	n.listenErr = ""
	n.server = nil
	n.logger.Debug("worker restarting")
	n.appendLog(formular.LogInfo, "worker restarting")
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		n.worker = nil
		old.Stop()
	}
	if n.formError != "" {
		_ = n.updateLabel()
		return
	}
	if !NetworkIsReady(n.network) {
		n.logger.Debug("waiting for network")
		n.appendLog(formular.LogWarn, "waiting for network")
		n.sendLogsBlock()
		_ = n.updateLabel()
		return
	}

	cfg := dnsServerWorkerConfig{
		config:   n.config,
		network:  n.network,
		upstream: n.upstream,
		spawner:  n.dnsSpawner(),
	}
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: dnsServerStart{id: id, cfg: cfg}})
		}()
		_ = n.updateLabel()
		return
	}
	n.startServer(id, cfg)
	_ = n.updateLabel()
}

func (n *dnsServerNode) startServer(id uint64, cfg dnsServerWorkerConfig) {
	addr := cfg.config.addr()
	n.logger.Debugf("worker starting on %s/%s", cfg.config.network, addr)
	n.appendLog(formular.LogInfo, "worker starting on "+cfg.config.network+"/"+addr)
	n.sendLogsBlock()
	ctx := context.Background()
	if n.engine != nil && n.engine.ctx != nil {
		ctx = n.engine.ctx
	}
	worker, err := n.NewRestartableWorker("dns server", func(stop <-chan struct{}) {
		runDNSServerWorker(ctx, n.logger, n.w, n.id, stop, id, cfg)
	})
	if err != nil {
		n.listenErr = err.Error()
		_ = n.updateLabel()
		return
	}
	n.worker = worker
}

func (n *dnsServerNode) stopServer() {
	n.restartID++
	n.server = nil
	n.listenErr = ""
	if n.worker != nil {
		n.logger.Debug("worker stopping")
		n.worker.Stop()
		n.worker = nil
	}
	_ = n.updateLabel()
}

func (n *dnsServerNode) attachUpstream() {
	if n.server == nil {
		return
	}
	if DNSIsReady(n.upstream) {
		n.server.Attach(n.upstream)
		return
	}
	n.server.Detach()
}

func (n *dnsServerNode) dnsSpawner() gonnect.Spawner {
	if n.workerSpawner.tracker == nil {
		return nil
	}
	return n.workerSpawner
}

func (n *dnsServerNode) onServerReady(payload dnsServerReady) {
	if payload.id != n.restartID || n.worker == nil {
		if payload.server != nil {
			_ = payload.server.Close()
		}
		return
	}
	n.server = payload.server
	n.listenErr = ""
	n.attachUpstream()
	n.appendLog(formular.LogInfo, "listening on "+payload.addr)
	n.sendLogsBlock()
	_ = n.updateLabel()
}

func (n *dnsServerNode) onServerRuntimeFailed(payload dnsServerRuntimeFailed) {
	if payload.id != n.restartID || n.worker == nil {
		return
	}
	n.server = nil
	n.listenErr = payload.err
	n.appendLog(formular.LogWarn, "server stopped: "+payload.err)
	n.sendLogsBlock()
	_ = n.updateLabel()
}

func runDNSServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, id uint64, cfg dnsServerWorkerConfig) {
	addr := cfg.config.addr()
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return
		default:
		}

		listenCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}()
		conn, err := cfg.network.ListenPacket(listenCtx, cfg.config.network, addr)
		cancel()
		close(done)
		if err != nil {
			logger.Debugf("listen failed on %s/%s: %v", cfg.config.network, addr, err)
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: dnsServerRuntimeFailed{id: id, err: err.Error()}})
			sendDNSServerLog(w, node, formular.LogDebug, "listen failed: "+err.Error())
			if !sleepOrStopped(stop, time.Second) {
				logger.Debug("worker stopped")
				return
			}
			continue
		}

		errCh := make(chan error, 1)
		reporting := dnsServerPacketConn{PacketConn: conn, errCh: errCh}
		server := dnsServerNewServer(reporting, cfg.upstream, cfg.spawner)
		logger.Debugf("listening on %s/%s", cfg.config.network, addr)
		w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: dnsServerReady{id: id, server: server, addr: fmt.Sprintf("%s/%s", cfg.config.network, addr)}})

		select {
		case <-stop:
			_ = server.Close()
			logger.Debug("worker stopped")
			return
		case err := <-errCh:
			_ = server.Close()
			if err == nil {
				err = net.ErrClosed
			}
			logger.Debugf("server stopped on %s/%s: %v", cfg.config.network, addr, err)
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: dnsServerRuntimeFailed{id: id, err: err.Error()}})
			if !sleepOrStopped(stop, time.Second) {
				logger.Debug("worker stopped")
				return
			}
		}
	}
}

func sendDNSServerLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: dnsServerLog{level: level, text: text}})
}

type dnsServerPacketConn struct {
	gonnect.PacketConn
	errCh chan<- error
}

func (c dnsServerPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(b)
	if err != nil {
		select {
		case c.errCh <- err:
		default:
		}
	}
	return n, addr, err
}
