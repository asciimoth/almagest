package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/asciimoth/almagest/bin/api"
)

// client owns one admin API connection. It queues asynchronous events received
// while waiting for a matching response and exposes them to watch commands.
type client struct {
	transport     apiTransport
	nextID        atomic.Uint64
	pendingEvents []api.Message
}

func newClient(transport apiTransport) *client {
	return &client{transport: transport}
}

func (c *client) Close() error {
	return c.transport.Close()
}

func (c *client) request(ctx context.Context, surface api.Surface, method string, payload any) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	msg, err := api.NewRequest(id, surface, method, payload)
	if err != nil {
		return nil, err
	}
	if err := c.transport.Write(ctx, msg); err != nil {
		return nil, err
	}
	for {
		msg, err := c.transport.Read(ctx)
		if err != nil {
			return nil, err
		}
		if msg.Kind == api.MessageEvent {
			c.pendingEvents = append(c.pendingEvents, msg)
			continue
		}
		if msg.Kind != api.MessageResponse || msg.ID != id {
			continue
		}
		if msg.Error != nil {
			return nil, apiError{err: msg.Error}
		}
		if len(msg.Result) == 0 {
			return json.RawMessage(`{}`), nil
		}
		return msg.Result, nil
	}
}

func (c *client) nextEvent(ctx context.Context) (api.Message, error) {
	if len(c.pendingEvents) > 0 {
		msg := c.pendingEvents[0]
		c.pendingEvents = c.pendingEvents[1:]
		return msg, nil
	}
	for {
		msg, err := c.transport.Read(ctx)
		if err != nil {
			return api.Message{}, err
		}
		if msg.Kind == api.MessageEvent {
			return msg, nil
		}
	}
}

type apiError struct {
	err *api.Error
}

func (e apiError) Error() string {
	return fmt.Sprintf("%s: %s", e.err.Code, e.err.Message)
}
