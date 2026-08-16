package main

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/kataras/golog"
)

func (a *AdminAPI) serveAPIStream(
	ctx context.Context,
	conn net.Conn,
	engine almagest.Engine,
	sys sysnet.System,
	logf *LogFactory,
) {
	_ = sys
	defer conn.Close() //nolint

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	defer close(done)

	codec := api.NewReadWriteCodec(conn)
	session := a.newAPISession(engine, logf, codec.Write)
	defer session.close()
	if err := session.sendProjectVersionStatus(); err != nil {
		return
	}

	for {
		msg, err := codec.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				golog.Debugf("Admin API stream read failed: %v", err)
			}
			return
		}

		response, err := session.handleMessage(msg)
		if err != nil {
			if err := writeStreamError(msg, api.ErrorInternal, err.Error(), codec.Write); err != nil {
				return
			}
			continue
		}
		if response.Error == nil && api.RequiresMutationLease(msg.Surface, msg.Method) {
			a.configAutosaver.Schedule()
		}
		if err := codec.Write(response); err != nil {
			return
		}
	}
}

func writeStreamError(
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
