package main

import (
	"context"
	"net/http"
	"sync"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/coder/websocket"
	"github.com/kataras/golog"
)

func (a *AdminAPI) registerAPIHandlers(
	ctx context.Context,
	mux *http.ServeMux,
	engine almagest.Engine,
	sys sysnet.System,
	logf *LogFactory,
) {
	mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
		a.serveWebsocket(ctx, w, r, engine, sys, logf)
	})
}

func (a *AdminAPI) serveWebsocket(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	engine almagest.Engine,
	sys sysnet.System,
	logf *LogFactory,
) {
	_ = sys

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "[::1]:*"},
	})
	if err != nil {
		golog.Debugf("WebSocket accept failed: %v", err)
		return
	}
	defer conn.CloseNow() //nolint

	var writeMu sync.Mutex
	writeMessage := func(msg api.Message) error {
		data, err := api.MarshalMessage(msg)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		return conn.Write(ctx, websocket.MessageText, data)
	}

	session := a.newAPISession(engine, logf, writeMessage)
	defer session.close()
	if err := session.sendProjectVersionStatus(); err != nil {
		return
	}

	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			continue
		}

		msg, err := api.UnmarshalMessage(data)
		if err != nil {
			if err := writeWebsocketError(api.Message{}, api.ErrorInvalidPayload, err.Error(), writeMessage); err != nil {
				return
			}
			continue
		}

		response, err := session.handleMessage(msg)
		if err != nil {
			if err := writeWebsocketError(msg, api.ErrorInternal, err.Error(), writeMessage); err != nil {
				return
			}
			continue
		}
		if response.Error == nil && api.RequiresMutationLease(msg.Surface, msg.Method) {
			a.configAutosaver.Schedule()
		}
		if err := writeMessage(response); err != nil {
			return
		}
	}
}

func (s *apiSession) sendProjectVersionStatus() error {
	event, err := api.NewEvent(
		api.SurfaceAdmin,
		string(api.EventAdminProjectVersionStatus),
		s.admin.projectVersionStatus,
	)
	if err != nil {
		return err
	}
	return s.send(event)
}

func writeWebsocketError(
	msg api.Message,
	code api.ErrorCode,
	message string,
	writeMessage func(api.Message) error,
) error {
	response, err := newRequestError(msg.ID, code, message)
	if err != nil {
		return err
	}
	return writeMessage(response)
}
