package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/coder/websocket"
	"github.com/kataras/golog"
)

//go:embed static
var webStatic embed.FS

type AdminAPI struct {
	webListener net.Listener
	webServer   *http.Server
}

func (a *AdminAPI) Stop() {
	if a.webServer != nil {
		golog.Debug("Closing web admin server")
		_ = a.webServer.Close()
	}
	if a.webListener != nil {
		golog.Debug("Closing web admin listener")
		_ = a.webListener.Close()
	}
}

func (a *AdminAPI) Run(
	ctx context.Context,
	cancel context.CancelFunc,
	wg *sync.WaitGroup,
	engine lib.Engine,
	sys sysnet.SysBuilder,
) {
	if a.webListener != nil {
		mux := http.NewServeMux()
		static, err := fs.Sub(webStatic, "static")
		if err != nil {
			golog.Errorf("Static web admin setup failed: %v", err)
			cancel()
			return
		}
		mux.Handle("/", http.FileServer(http.FS(static)))
		mux.HandleFunc("/api/ws", func(w http.ResponseWriter, r *http.Request) {
			a.serveWebsocket(ctx, w, r, engine, sys)
		})

		server := &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		a.webServer = server

		wg.Go(func() {
			<-ctx.Done()
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutdownCancel()
			_ = server.Shutdown(shutdownCtx)
		})

		wg.Go(func() {
			golog.Infof("Starting web admin listener on http://%s", a.webListener.Addr())
			if err := server.Serve(a.webListener); err != nil &&
				!errors.Is(err, http.ErrServerClosed) &&
				!errors.Is(err, net.ErrClosed) {
				golog.Errorf("Web admin server failed: %v", err)
				cancel()
			}
		})
	}
}

func (a *AdminAPI) serveWebsocket(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	engine lib.Engine,
	sys sysnet.SysBuilder,
) {
	_ = engine
	_ = sys

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*", "[::1]:*"},
	})
	if err != nil {
		golog.Debugf("WebSocket accept failed: %v", err)
		return
	}
	defer conn.CloseNow() //nolint

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
			_ = writeWebsocketError(ctx, conn, "", api.ErrorInvalidPayload, err.Error())
			continue
		}

		response, err := handleWebsocketMessage(msg)
		if err != nil {
			_ = writeWebsocketError(ctx, conn, msg.ID, api.ErrorInternal, err.Error())
			continue
		}
		if err := conn.Write(ctx, websocket.MessageText, response); err != nil {
			return
		}
	}
}

func handleWebsocketMessage(msg api.Message) ([]byte, error) {
	if msg.Kind != api.MessageRequest {
		response, err := api.NewErrorResponse(
			msg.ID,
			api.ErrorBadRequest,
			"websocket endpoint accepts request messages",
			nil,
		)
		if err != nil {
			return nil, err
		}
		return api.MarshalMessage(response)
	}

	switch {
	case msg.Surface == api.SurfaceAdmin && msg.Method == string(api.MethodAdminHello):
		if _, err := api.DecodePayload[api.HelloRequest](msg.Payload); err != nil {
			return marshalRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		response, err := api.NewResponse(msg.ID, api.HelloResponse{
			Version: api.Version,
			Surfaces: map[string]any{
				string(api.SurfaceAdmin): map[string]any{
					"methods": api.AdminMethods(),
				},
				string(api.SurfaceEngine): map[string]any{
					"methods": api.EngineMethods(),
				},
			},
		})
		if err != nil {
			return nil, err
		}
		return api.MarshalMessage(response)

	case msg.Surface == api.SurfaceAdmin && msg.Method == string(api.MethodAdminPing):
		req, err := api.DecodePayload[api.PingRequest](msg.Payload)
		if err != nil {
			return marshalRequestError(msg.ID, api.ErrorInvalidPayload, err.Error())
		}
		response, err := api.NewResponse(msg.ID, api.PingResponse(req))
		if err != nil {
			return nil, err
		}
		return api.MarshalMessage(response)

	default:
		return marshalRequestError(msg.ID, api.ErrorInvalidMethod, "api method is not implemented")
	}
}

func writeWebsocketError(
	ctx context.Context,
	conn *websocket.Conn,
	id string,
	code api.ErrorCode,
	message string,
) error {
	data, err := marshalRequestError(id, code, message)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func marshalRequestError(id string, code api.ErrorCode, message string) ([]byte, error) {
	if id == "" {
		id = "0"
	}
	response, err := api.NewErrorResponse(id, code, message, nil)
	if err != nil {
		return nil, err
	}
	return api.MarshalMessage(response)
}

func PrepareAdminAPI(cfg *Config) (*AdminAPI, error) {
	api := AdminAPI{}
	if cfg.Protected.Admin.Web != 0 {
		listener, err := net.Listen(
			"tcp",
			net.JoinHostPort("127.0.0.1", strconv.Itoa(int(cfg.Protected.Admin.Web))),
		)
		if err != nil {
			return nil, err
		}
		api.webListener = listener
	}
	return &api, nil
}
