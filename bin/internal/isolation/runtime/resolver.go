package runtime

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
)

// OutDNS forwards packets to the backend's existing OutDNS provider. It does
// not reconstruct provider routing from a snapshot or select a public fallback.
// Four persistent packet channels bound concurrent queries and avoid creating
// a broker resource and mutation receipt for every successful DNS request.
// The caller owns Close; closing the runtime client also stops these workers.
func (c *Client) OutDNS(ctx context.Context) (gdns.Interface, error) {
	first, err := c.openResolver(ctx)
	if err != nil {
		return nil, err
	}
	connections := []*Conn{first}
	for len(connections) < 4 {
		connection, err := c.openResolver(ctx)
		if errors.Is(err, protocol.ErrLimitExceeded) {
			break // Preserve DNS with a smaller protected socket quota.
		}
		if err != nil {
			for _, opened := range connections {
				_ = opened.Close()
			}
			return nil, err
		}
		connections = append(connections, connection)
	}
	root, cancel := context.WithCancel(context.Background())
	d := &resolver{client: c, requests: make(chan gdns.Request), cancel: cancel}
	for _, initial := range connections {
		d.workers.Go(func() { d.run(root, initial) })
	}
	d.workers.Go(func() {
		select {
		case <-c.peer.Done():
			cancel()
		case <-root.Done():
		}
	})
	return d, nil
}

func (c *Client) openResolver(ctx context.Context) (*Conn, error) {
	result, err := c.openResource(ctx, protocol.OperationOpenSocket, protocol.OpenSocketRequest{Meta: c.meta(true), Options: protocol.SocketOptions{Purpose: protocol.NetworkOutbound, Kind: protocol.SocketResolver}})
	if err != nil {
		return nil, err
	}
	if result.Outcome != protocol.OutcomeCommitted {
		if result.Code != "" {
			return nil, decodeError(result.Code)
		}
		return nil, protocol.ErrRecoveryRequired
	}
	if err := result.Resource.Validate(c.session, protocol.ResourceSocket, c.epoch.Load(), 1); err != nil {
		return nil, err
	}
	return &Conn{client: c, ref: result.Resource, packet: true}, nil
}

type resolver struct {
	client   *Client
	requests chan gdns.Request
	cancel   context.CancelFunc
	workers  sync.WaitGroup
}

func (r *resolver) Requests() chan<- gdns.Request { return r.requests }
func (r *resolver) Close() error                  { r.cancel(); r.workers.Wait(); return nil }

func (r *resolver) run(root context.Context, connection *Conn) {
	defer func() {
		if connection != nil {
			_ = connection.Close()
		}
	}()
	for {
		select {
		case <-root.Done():
			return
		case request := <-r.requests:
			requestContext := request.Context
			if requestContext == nil {
				requestContext = context.Background()
			}
			ctx, cancel := context.WithTimeout(requestContext, 5*time.Second)
			stop := context.AfterFunc(root, cancel)
			var response *gdns.Message
			var err error
			if connection == nil {
				connection, err = r.client.openResolver(ctx)
			}
			if err == nil {
				response, err = resolverQuery(ctx, connection, request.Message)
			}
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if root.Err() != nil {
				err = net.ErrClosed
			}
			stop()
			cancel()
			if err != nil && connection != nil {
				_ = connection.Close()
				connection = nil
			}
			// Query uses a buffered reply. Deliver the terminal error even when
			// Close has canceled root, so a background-context caller is woken.
			result := gdns.Response{Message: response, Err: err}
			select {
			case request.Reply <- result:
			default:
				select {
				case request.Reply <- result:
				case <-requestContext.Done():
				case <-root.Done():
				}
			}
		}
	}
}

func resolverQuery(ctx context.Context, connection *Conn, message *gdns.Message) (*gdns.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if message == nil || message.Response {
		return nil, protocol.ErrInvalidContract
	}
	packet, err := gdns.Pack(message)
	if err != nil {
		return nil, err
	}
	if len(packet) > protocol.MaxPacketRecord {
		return nil, protocol.ErrLimitExceeded
	}
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	woken := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = connection.SetDeadline(time.Now()); close(woken) })
	defer func() {
		if !stop() {
			<-woken
		}
	}()
	if _, err := connection.Write(packet); err != nil {
		return nil, err
	}
	buffer := make([]byte, protocol.MaxPacketRecord)
	n, err := connection.Read(buffer)
	if err != nil {
		return nil, err
	}
	if n < 12 {
		return nil, net.ErrClosed
	}
	return gdns.Unpack(buffer[:n])
}
