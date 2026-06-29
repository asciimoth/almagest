package almagest

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

type udpPingLog struct {
	level string
	text  string
}

type udpPingServerWorker = restartableWorker

type udpPingServerStart struct {
	id  uint64
	cfg udpPingConfig
	net NetworkCloser
}

type udpPingClientRequest struct {
	id  uint64
	cfg udpPingConfig
	net NetworkCloser
}

type udpPingClientResult struct {
	id     uint64
	status string
	resp   string
	err    string
}

func runUDPPingServerWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, stop <-chan struct{}, cfg udpPingConfig, netw NetworkCloser) {
	conn, err := openUDPConn(ctx, netw, cfg)
	if err != nil {
		logger.Debugf("udp server open failed: %v", err)
		sendUDPPingLog(w, node, formular.LogError, "open failed: "+err.Error())
		return
	}
	defer conn.Close() //nolint:errcheck

	logger.Debugf("udp server listening on %s via %s %s", conn.LocalAddr(), cfg.network, cfg.socketMode)
	sendUDPPingLog(w, node, formular.LogInfo, "listening on "+conn.LocalAddr().String())

	go func() {
		select {
		case <-stop:
			_ = conn.Close()
		case <-ctx.Done():
			_ = conn.Close()
		}
	}()

	buf := make([]byte, 64*1024)
	for {
		n, from, err := readUDP(conn, cfg.socketMode, buf)
		if err != nil {
			if stopped(stop) || errors.Is(err, net.ErrClosed) {
				logger.Debug("udp server stopped")
				return
			}
			logger.Debugf("udp server read failed: %v", err)
			sendUDPPingLog(w, node, formular.LogWarn, "read failed: "+err.Error())
			return
		}
		request := string(buf[:n])
		response := "pong:" + request
		if _, err := writeUDP(conn, cfg.socketMode, []byte(response), from); err != nil {
			if stopped(stop) || errors.Is(err, net.ErrClosed) {
				logger.Debug("udp server stopped")
				return
			}
			logger.Debugf("udp server write failed: %v", err)
			sendUDPPingLog(w, node, formular.LogWarn, "write failed: "+err.Error())
			return
		}
		peer := "<connected>"
		if from != nil {
			peer = from.String()
		}
		logger.Debugf("udp pong to %s: %q", peer, response)
		sendUDPPingLog(w, node, formular.LogInfo, fmt.Sprintf("pong to %s: %q", peer, response))
	}
}

func runUDPPingClientWorker(ctx context.Context, logger pasta.Logger, w *pasta.Workspace, node uint64, jobs <-chan udpPingClientRequest, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case req := <-jobs:
			result := doUDPPing(ctx, req)
			if result.err != "" {
				logger.Debugf("udp ping %d failed: %s", req.id, result.err)
				sendUDPPingLog(w, node, formular.LogWarn, "ping failed: "+result.err)
			} else {
				logger.Debugf("udp ping %d complete: %s", req.id, result.resp)
				sendUDPPingLog(w, node, formular.LogInfo, "ping complete: "+result.resp)
			}
			w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: result})
		}
	}
}

func doUDPPing(parent context.Context, req udpPingClientRequest) udpPingClientResult {
	if req.net == nil {
		return udpPingClientResult{id: req.id, err: "network is not ready"}
	}
	ctx, cancel := context.WithTimeout(parent, req.cfg.timeout)
	defer cancel()
	conn, err := openUDPConn(ctx, req.net, req.cfg)
	if err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	defer conn.Close() //nolint:errcheck

	if err := conn.SetDeadline(time.Now().Add(req.cfg.timeout)); err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	if _, err := writeUDP(conn, req.cfg.socketMode, []byte(req.cfg.payload), udpAddr(req.cfg.network, req.cfg.remoteAddr())); err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	buf := make([]byte, 64*1024)
	n, _, err := readUDP(conn, req.cfg.socketMode, buf)
	if err != nil {
		return udpPingClientResult{id: req.id, err: err.Error()}
	}
	resp := string(buf[:n])
	want := "pong:" + req.cfg.payload
	if resp != want {
		return udpPingClientResult{id: req.id, resp: resp, err: fmt.Sprintf("unexpected response %q, want %q", resp, want)}
	}
	return udpPingClientResult{id: req.id, status: "ok", resp: resp}
}

func sendUDPPingLog(w *pasta.Workspace, node uint64, level, text string) {
	w.SendInbox(pasta.InboxMessage{ReceiverNode: node, Payload: udpPingLog{level: level, text: text}})
}

func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}
