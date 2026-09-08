package main

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/asciimoth/almagest/bin/api"
	"github.com/coder/websocket"
)

// apiTransport is the common request/event stream used by Unix sockets and
// WebSockets.
type apiTransport interface {
	Read(context.Context) (api.Message, error)
	Write(context.Context, api.Message) error
	Close() error
}

type dialFunc func(context.Context, endpoint) (apiTransport, error)

func realDial(ctx context.Context, ep endpoint) (apiTransport, error) {
	switch ep.kind {
	case endpointUnix:
		var d net.Dialer
		conn, err := d.DialContext(ctx, "unix", ep.path)
		if err != nil {
			return nil, err
		}
		return &streamTransport{conn: conn, codec: api.NewReadWriteCodec(conn)}, nil
	case endpointWeb:
		conn, _, err := websocket.Dial(ctx, ep.url, nil)
		if err != nil {
			return nil, err
		}
		// Snapshot export responses can be larger than the WebSocket package's
		// 32 KiB default. Unix-socket responses do not have that limit.
		conn.SetReadLimit(-1)
		return &websocketTransport{conn: conn}, nil
	default:
		panic("unknown endpoint kind")
	}
}

type streamTransport struct {
	conn  net.Conn
	codec *api.Codec
}

func (t *streamTransport) Read(ctx context.Context) (api.Message, error) {
	restore := setConnDeadlineUntilContext(ctx, t.conn.SetReadDeadline)
	defer restore()
	msg, err := t.codec.Read()
	if err != nil {
		return api.Message{}, contextErrorForTimeout(ctx, err)
	}
	return msg, nil
}

func (t *streamTransport) Write(ctx context.Context, msg api.Message) error {
	restore := setConnDeadlineUntilContext(ctx, t.conn.SetWriteDeadline)
	defer restore()
	err := t.codec.Write(msg)
	return contextErrorForTimeout(ctx, err)
}

func (t *streamTransport) Close() error {
	return t.conn.Close()
}

type websocketTransport struct {
	conn *websocket.Conn
}

func (t *websocketTransport) Read(ctx context.Context) (api.Message, error) {
	typ, data, err := t.conn.Read(ctx)
	if err != nil {
		return api.Message{}, err
	}
	if typ != websocket.MessageText {
		return t.Read(ctx)
	}
	return api.UnmarshalMessage(data)
}

func (t *websocketTransport) Write(ctx context.Context, msg api.Message) error {
	data, err := api.MarshalMessage(msg)
	if err != nil {
		return err
	}
	return t.conn.Write(ctx, websocket.MessageText, data)
}

func (t *websocketTransport) Close() error {
	return t.conn.Close(websocket.StatusNormalClosure, "")
}

func setConnDeadlineUntilContext(ctx context.Context, setDeadline func(time.Time) error) func() {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = setDeadline(deadline)
	}
	done := ctx.Done()
	if done == nil {
		return func() {
			_ = setDeadline(time.Time{})
		}
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			_ = setDeadline(time.Now())
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-stopped
		_ = setDeadline(time.Time{})
	}
}

func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func contextErrorForTimeout(ctx context.Context, err error) error {
	if err == nil || ctx == nil || !isTimeoutError(err) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return err
}
