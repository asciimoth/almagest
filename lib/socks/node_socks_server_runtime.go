package socksnodes

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/pasta/pasta"
)

const (
	socksServerRestartDelay = time.Second
	// socksServerProxyStartGrace gives restored proxy links a short window to
	// deliver their network before the server starts with a rejecting proxy.
	socksServerProxyStartGrace = 250 * time.Millisecond
)

type socksServerWorker = restartableWorker

type socksServerLog struct {
	level string
	text  string
}

type socksServerStart struct {
	id  uint64
	cfg socksServerWorkerConfig
}

// socksServerDeferredStart resumes startup after the proxy-link grace window.
// It lets restore settle without blocking the workspace callback that received
// the listen network.
type socksServerDeferredStart struct {
	id uint64
}

type socksServerWorkerConfig struct {
	config        socksServerConfig
	listenNetwork NetworkCloser
	proxyNetwork  gonnect.Network
	pool          bufpool.Pool
}

func (n *socksServerNode) restartWorker(reason string) {
	n.restartID++
	id := n.restartID
	n.Logger.Debugf("socks worker restarting: %s", reason)
	n.appendLog(formular.LogDebug, "worker restarting: "+reason)
	n.sendLogsBlock()

	old := n.worker
	if old != nil {
		n.worker = nil
		old.Stop()
	}

	// If a proxy link already exists, do not start with RejectNetwork; the proxy
	// payload is expected to arrive as restore finishes.
	if n.workerStartBlocked() {
		return
	}
	if n.proxyNetwork == nil {
		n.deferWorkerStart(id, old)
		return
	}

	n.startWorkerIfReady(id, old)
}

// startWorkerIfReady is shared by immediate starts, starts after the old worker
// exits, and deferred starts after the proxy grace window.
func (n *socksServerNode) startWorkerIfReady(id uint64, old *socksServerWorker) {
	if id != n.restartID || n.worker != nil {
		return
	}
	if n.workerStartBlocked() {
		return
	}
	cfg := n.workerConfig()
	if old != nil {
		go func() {
			<-old.Done()
			n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: socksServerStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

// workerStartBlocked reports missing required runtime inputs and emits the
// same user-visible log line regardless of which startup path checked it.
func (n *socksServerNode) workerStartBlocked() bool {
	if n.listenNetwork == nil {
		n.Logger.Debug("socks worker waiting for listen network")
		n.appendLog(formular.LogWarn, "waiting for listen network")
		n.sendLogsBlock()
		return true
	}
	if n.proxyNetwork == nil && n.PortHasLinks(n.proxy) {
		n.Logger.Debug("socks worker waiting for proxy network")
		n.appendLog(formular.LogWarn, "waiting for proxy network")
		n.sendLogsBlock()
		return true
	}
	return false
}

// deferWorkerStart waits for any old worker to stop, then gives restore a short
// chance to attach the optional proxy link before falling back to RejectNetwork.
func (n *socksServerNode) deferWorkerStart(id uint64, old *socksServerWorker) {
	n.Logger.Debug("socks worker waiting briefly for proxy link")
	n.appendLog(formular.LogDebug, "waiting briefly for proxy link")
	n.sendLogsBlock()
	go func() {
		if old != nil {
			<-old.Done()
		}
		timer := time.NewTimer(socksServerProxyStartGrace)
		defer timer.Stop()
		<-timer.C
		n.W.SendInbox(pasta.InboxMessage{ReceiverNode: n.ID, Payload: socksServerDeferredStart{id: id}})
	}()
}

// workerConfig snapshots the current runtime networks for a worker start. A
// missing proxy network is intentional only when there is no proxy link waiting
// to deliver a network payload.
func (n *socksServerNode) workerConfig() socksServerWorkerConfig {
	proxy := gonnect.Network(&gonnect.RejectNetwork{})
	if n.proxyNetwork != nil {
		proxy = n.proxyNetwork
	}
	return socksServerWorkerConfig{
		config:        n.config,
		listenNetwork: n.listenNetwork,
		proxyNetwork:  proxy,
		pool:          enginePool(n.Runtime),
	}
}

func (n *socksServerNode) startWorker(cfg socksServerWorkerConfig) {
	n.Logger.Debug("socks worker starting")
	worker, err := n.NewRestartableWorker("socks server", func(stop <-chan struct{}) {
		runSocksServerWorker(n.Context(), n.Logger, n.W, n.ID, stop, cfg, n.WorkerSpawner)
	})
	if err != nil {
		return
	}
	n.worker = worker
}

func (n *socksServerNode) stopWorker() {
	if n.worker != nil {
		n.Logger.Debug("socks worker stopping")
		n.worker.Stop()
		n.worker = nil
	}
}

func runSocksServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg socksServerWorkerConfig, spawner WorkerSpawner) {
	for {
		select {
		case <-stop:
			logger.Debug("socks worker stopped")
			return
		default:
		}

		if !cfg.config.hasListener() {
			sendSocksServerLog(w, node, formular.LogWarn, "no SOCKS listener configured")
			if !sleepOrStopped(stop, socksServerRestartDelay) {
				logger.Debug("socks worker stopped")
				return
			}
			continue
		}

		err := runSocksServerOnce(ctx, logger, w, node, stop, cfg, spawner)
		if err != nil {
			logger.Debugf("socks server stopped: %v", err)
			sendSocksServerLog(w, node, formular.LogDebug, "server stopped: "+err.Error())
		}
		if !sleepOrStopped(stop, socksServerRestartDelay) {
			logger.Debug("socks worker stopped")
			return
		}
	}
}

func runSocksServerOnce(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg socksServerWorkerConfig, spawner WorkerSpawner) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	// Nil spawners are intentional here; see the Gonnect spawner note in helpers.go.
	listenNetwork := gonnect.DetachNetwork(cfg.listenNetwork, nil, nil)
	defer listenNetwork.Close() //nolint
	proxyNetwork := gonnect.DetachNetwork(cfg.proxyNetwork, nil, nil)
	defer proxyNetwork.Close() //nolint
	cfg.listenNetwork = listenNetwork
	cfg.proxyNetwork = proxyNetwork

	server := buildSocksGoServer(logger, cfg)
	errs := make(chan error, 2)
	started := 0
	closers := make([]func(), 0, 2)

	if cfg.config.socksPort > 0 {
		addr := net.JoinHostPort(hostOrDefault(cfg.config.socksHost), strconv.Itoa(cfg.config.socksPort))
		ln, err := cfg.listenNetwork.Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen failed on %s: %w", addr, err)
		}
		closers = append(closers, func() { _ = ln.Close() })
		started++
		logger.Debugf("socks listening on %s", addr)
		sendSocksServerLog(w, node, formular.LogInfo, "socks listening on "+addr)
		if _, err := spawner.Spawn(func() {
			serveSocksListener(ctx, logger, server, ln, errs, spawner)
		}, "socks listener"); err != nil {
			for _, closeFn := range closers {
				closeFn()
			}
			return err
		}
	}

	if cfg.config.wsPort > 0 {
		addr := net.JoinHostPort(hostOrDefault(cfg.config.wsHost), strconv.Itoa(cfg.config.wsPort))
		ln, err := cfg.listenNetwork.Listen(ctx, "tcp", addr)
		if err != nil {
			for _, closeFn := range closers {
				closeFn()
			}
			return fmt.Errorf("socks+ws listen failed on %s: %w", addr, err)
		}
		httpServer := &http.Server{
			Handler:           socksWebSocketHandler(ctx, logger, server, cfg.config.wsPath),
			ReadHeaderTimeout: 5 * time.Second,
		}
		closers = append(closers, func() {
			_ = ln.Close()
			_ = httpServer.Shutdown(context.Background())
		})
		started++
		logger.Debugf("socks+ws listening on %s%s", addr, cfg.config.wsPath)
		sendSocksServerLog(w, node, formular.LogInfo, "socks+ws listening on "+addr+cfg.config.wsPath)
		if _, err := spawner.Spawn(func() {
			err := httpServer.Serve(ln)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}, "socks websocket server"); err != nil {
			for _, closeFn := range closers {
				closeFn()
			}
			return err
		}
	}

	if started == 0 {
		return errors.New("no SOCKS listener configured")
	}

	select {
	case <-stop:
		for _, closeFn := range closers {
			closeFn()
		}
		logger.Debug("socks worker stopped")
		return nil
	case <-ctx.Done():
		for _, closeFn := range closers {
			closeFn()
		}
		return ctx.Err()
	case err := <-errs:
		for _, closeFn := range closers {
			closeFn()
		}
		return err
	}
}

func sendSocksServerLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: socksServerLog{level: level, text: text}})
}
