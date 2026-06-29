package almagest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

type httpServerLog struct {
	level string
	text  string
}

type httpServerStart struct {
	id  uint64
	cfg httpServerConfig
}

type httpServerWorker = restartableWorker

type httpServerConfig struct {
	host     string
	port     int
	response string
	network  NetworkCloser
}

func (n *httpServerNode) restartWorker() {
	n.restartID++
	id := n.restartID
	n.logger.Debug("worker restarting")
	n.appendLog(formular.LogInfo, "worker restarting")
	n.sendLogsBlock()
	old := n.worker
	if old != nil {
		n.worker = nil
		old.Stop()
	}
	if n.network == nil {
		n.logger.Debug("waiting for network")
		n.appendLog(formular.LogWarn, "waiting for network")
		n.sendLogsBlock()
		return
	}
	cfg := httpServerConfig{host: n.host, port: n.port, response: n.response, network: n.network}
	if old != nil {
		go func() {
			<-old.done
			n.w.SendInbox(pasta.InboxMessage{ReceiverNode: n.id, Payload: httpServerStart{id: id, cfg: cfg}})
		}()
		return
	}
	n.startWorker(cfg)
}

func (n *httpServerNode) startWorker(cfg httpServerConfig) {
	n.logger.Debugf("worker starting on %s", net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port)))
	n.worker = newRestartableWorker(func(stop <-chan struct{}) {
		runHTTPServerWorker(n.engine.ctx, n.logger, n.w, n.id, stop, cfg)
	})
}

func (n *httpServerNode) stopWorker() {
	if n.worker != nil {
		n.logger.Debug("worker stopping")
		n.worker.Stop()
		n.worker = nil
	}
}

func runHTTPServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg httpServerConfig) {
	addr := net.JoinHostPort(cfg.host, strconv.Itoa(cfg.port))
	for {
		select {
		case <-stop:
			logger.Debug("worker stopped")
			return
		default:
		}
		ctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			select {
			case <-stop:
				cancel()
			case <-done:
			}
		}()
		ln, err := cfg.network.Listen(ctx, "tcp", addr)
		cancel()
		close(done)
		if err != nil {
			logger.Debugf("listen failed on %s: %v", addr, err)
			sendHTTPServerLog(w, node, formular.LogDebug, "listen failed: "+err.Error())
			if !sleepOrStopped(stop, time.Second) {
				logger.Debug("worker stopped")
				return
			}
			continue
		}
		body := cfg.response
		if body == "" {
			body = "Debug http server response"
		}
		handler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			logger.Debugf("%s %s", r.Method, r.URL.String())
			sendHTTPServerLog(w, node, formular.LogInfo, fmt.Sprintf("%s %s", r.Method, r.URL.String()))
			_, _ = rw.Write([]byte(body))
		})
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		serveDone := make(chan error, 1)
		go func() { serveDone <- server.Serve(ln) }()
		logger.Debugf("listening on %s", addr)
		sendHTTPServerLog(w, node, formular.LogInfo, "listening on "+addr)
		select {
		case <-stop:
			_ = ln.Close()
			_ = server.Shutdown(context.Background())
			<-serveDone
			logger.Debug("worker stopped")
			return
		case err := <-serveDone:
			_ = ln.Close()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Debugf("server stopped: %v", err)
				sendHTTPServerLog(w, node, formular.LogWarn, "server stopped: "+err.Error())
			}
			if !sleepOrStopped(stop, time.Second) {
				logger.Debug("worker stopped")
				return
			}
		}
	}
}

func sendHTTPServerLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: httpServerLog{level: level, text: text}})
}
