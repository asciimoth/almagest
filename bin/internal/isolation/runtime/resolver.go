package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	gdns "github.com/asciimoth/gonnect/dns"
)

// dnsResolver fixes reverse lookups for a DNS-backed gonnect resolver. The
// upstream adapter sends a literal IP as the PTR question name. Native
// resolvers instead convert the literal to its in-addr.arpa or ip6.arpa name.
type dnsResolver struct {
	*gdns.Resolver
	provider gdns.Interface
}

func newDNSResolver(provider gdns.Interface) *dnsResolver {
	return &dnsResolver{Resolver: gdns.NewResolver(provider), provider: provider}
}

func (r *dnsResolver) LookupAddr(ctx context.Context, address string) ([]string, error) {
	name, err := reverseDNSName(address)
	if err != nil {
		return nil, err
	}
	response, err := gdns.Query(ctx, r.provider, &gdns.Message{
		ID:               gdns.NextID(),
		RecursionDesired: true,
		Questions: []gdns.Question{{
			Name:  name,
			Type:  gdns.TypePTR,
			Class: gdns.ClassIN,
		}},
	})
	if err != nil {
		return nil, err
	}
	var names []string
	if response != nil && response.RCode == gdns.RCodeSuccess {
		for _, answer := range response.Answers {
			if answer.Type == gdns.TypePTR && (answer.Class == 0 || answer.Class == gdns.ClassIN) {
				names = append(names, string(answer.Data))
			}
		}
	}
	if len(names) == 0 {
		return nil, &net.DNSError{Name: address, Err: "no such host", IsNotFound: true}
	}
	return names, nil
}

func reverseDNSName(address string) (string, error) {
	addr, err := netip.ParseAddr(address)
	if err != nil || addr.Zone() != "" {
		return "", &net.DNSError{Name: address, Err: "invalid address"}
	}
	addr = addr.Unmap()
	if addr.Is4() {
		bytes := addr.As4()
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa.", bytes[3], bytes[2], bytes[1], bytes[0]), nil
	}
	bytes := addr.As16()
	const hex = "0123456789abcdef"
	var name strings.Builder
	name.Grow(73)
	for i := len(bytes) - 1; i >= 0; i-- {
		name.WriteByte(hex[bytes[i]&0x0f])
		name.WriteByte('.')
		name.WriteByte(hex[bytes[i]>>4])
		name.WriteByte('.')
	}
	name.WriteString("ip6.arpa.")
	return name.String(), nil
}

// OutDNS forwards packets to the backend's existing OutDNS provider. It does
// not reconstruct provider routing from a snapshot or select a public fallback.
// Four persistent packet channels bound concurrent queries and avoid creating
// a broker resource and mutation receipt for every successful DNS request.
// The caller owns Close; closing the runtime client also stops these workers.
func (c *Client) OutDNS(ctx context.Context) (gdns.Interface, error) {
	return c.openDNS(ctx, protocol.SocketResolver, protocol.NetworkOutbound)
}

// NetworkDNS forwards resolver queries through the selected native Network.
// Unlike OutDNS, it preserves local host data and other Network resolver rules.
func (c *Client) NetworkDNS(ctx context.Context, purpose protocol.NetworkPurpose) (gdns.Interface, error) {
	return c.openDNS(ctx, protocol.SocketNetworkResolver, purpose)
}

func (c *Client) openDNS(ctx context.Context, kind protocol.SocketKind, purpose protocol.NetworkPurpose) (gdns.Interface, error) {
	first, err := c.openResolver(ctx, kind, purpose)
	if err != nil {
		return nil, err
	}
	connections := []*Conn{first}
	for len(connections) < 4 {
		connection, err := c.openResolver(ctx, kind, purpose)
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
	d := &resolver{client: c, kind: kind, purpose: purpose, requests: make(chan gdns.Request), cancel: cancel}
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

func (c *Client) openResolver(ctx context.Context, kind protocol.SocketKind, purpose protocol.NetworkPurpose) (*Conn, error) {
	result, err := c.openResource(ctx, protocol.OperationOpenSocket, protocol.OpenSocketRequest{Meta: c.meta(true), Options: protocol.SocketOptions{Purpose: purpose, Kind: kind}})
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
	kind     protocol.SocketKind
	purpose  protocol.NetworkPurpose
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
				connection, err = r.client.openResolver(ctx, r.kind, r.purpose)
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
