package admingate

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
)

func testSession(t *testing.T) protocol.Session {
	t.Helper()
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Session{ID: id, Epoch: 7, Version: protocol.Version, PolicyDigest: [32]byte{1, 2, 3}}
}

type channelListener struct {
	connections chan net.Conn
	closed      chan struct{}
	once        sync.Once
}

func newChannelListener() *channelListener {
	return &channelListener{connections: make(chan net.Conn, MaxConnections+2), closed: make(chan struct{})}
}

func (l *channelListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *channelListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *channelListener) Addr() net.Addr { return testAddress("public") }

func (l *channelListener) connect(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client
	case <-l.closed:
		_ = client.Close()
		_ = server.Close()
		t.Fatal("listener closed")
		return nil
	}
}

type testAddress string

func (testAddress) Network() string  { return "test" }
func (a testAddress) String() string { return string(a) }

func newGatePair(t *testing.T, public []Endpoint, ids []uint16) (*Broker, *Runtime) {
	t.Helper()
	left, right := net.Pipe()
	session := testSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type brokerResult struct {
		broker *Broker
		err    error
	}
	result := make(chan brokerResult, 1)
	go func() {
		broker, err := NewBroker(ctx, left, session, public)
		result <- brokerResult{broker, err}
	}()
	runtime, err := NewRuntime(ctx, right, session, ids)
	if err != nil {
		t.Fatal(err)
	}
	created := <-result
	if created.err != nil {
		t.Fatal(created.err)
	}
	t.Cleanup(func() {
		_ = created.broker.Close()
		_ = runtime.Close()
		created.broker.Wait()
		runtime.Wait()
	})
	return created.broker, runtime
}

func TestGateRelaysOpaqueStreamsByFixedEndpoint(t *testing.T) {
	first, second := newChannelListener(), newChannelListener()
	_, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: first}, {ID: 2, Listener: second}}, []uint16{1, 2})
	for id, public := range map[uint16]*channelListener{1: first, 2: second} {
		listener, err := runtime.Listener(id)
		if err != nil {
			t.Fatal(err)
		}
		external := public.connect(t)
		relayed, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte{0, 1, 2, 0xff, byte(id)}
		go func() { _, _ = external.Write(payload) }()
		got := make([]byte, len(payload))
		if _, err = io.ReadFull(relayed, got); err != nil || string(got) != string(payload) {
			t.Fatalf("runtime read = %v, %v", got, err)
		}
		go func() { _, _ = relayed.Write([]byte("reply")) }()
		got = make([]byte, 5)
		if _, err = io.ReadFull(external, got); err != nil || string(got) != "reply" {
			t.Fatalf("public read = %q, %v", got, err)
		}
		_ = external.Close()
		_ = relayed.Close()
	}
	if _, err := runtime.Listener(99); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("unknown Listener error = %v", err)
	}
}

func TestPreparedBrokerPublishesOnlyAfterStart(t *testing.T) {
	left, right := net.Pipe()
	session := testSession(t)
	type result struct {
		broker *Broker
		err    error
	}
	created := make(chan result, 1)
	go func() {
		broker, err := NewPreparedBroker(context.Background(), left, session, []uint16{1})
		created <- result{broker: broker, err: err}
	}()
	runtime, err := NewRuntime(context.Background(), right, session, []uint16{1})
	if err != nil {
		t.Fatal(err)
	}
	got := <-created
	if got.err != nil {
		t.Fatal(got.err)
	}
	t.Cleanup(func() {
		_ = got.broker.Close()
		_ = runtime.Close()
		got.broker.Wait()
		runtime.Wait()
	})
	if err = got.broker.Start([]Endpoint{{ID: 2, Listener: newChannelListener()}}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("Start with changed endpoint ID = %v", err)
	}
	public := newChannelListener()
	if err = got.broker.Start([]Endpoint{{ID: 1, Listener: public}}); err != nil {
		t.Fatal(err)
	}
	if err = got.broker.Start([]Endpoint{{ID: 1, Listener: newChannelListener()}}); !errors.Is(err, protocol.ErrInvalidContract) {
		t.Fatalf("second Start = %v", err)
	}
	external := public.connect(t)
	relayed, err := runtime.Listener(1)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := relayed.Accept()
	if err != nil {
		t.Fatal(err)
	}
	_ = external.Close()
	_ = accepted.Close()
}

func TestGateKeepsHTTPAndOriginParsingInRuntime(t *testing.T) {
	public := newChannelListener()
	_, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: public}}, []uint16{1})
	listener, _ := runtime.Listener(1)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Origin") != "https://runtime.test" {
			http.Error(w, "origin rejected", http.StatusForbidden)
			return
		}
		_, _ = io.WriteString(w, "runtime parsed request")
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("server shutdown error = %v", err)
		}
	})

	client := public.connect(t)
	defer func() { _ = client.Close() }()
	_, _ = io.WriteString(client, "GET /admin HTTP/1.1\r\nHost: local\r\nOrigin: https://runtime.test\r\nConnection: close\r\n\r\n")
	response, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "runtime parsed request" {
		t.Fatalf("HTTP response = %d %q, %v", response.StatusCode, body, err)
	}
}

func TestSlowConnectionDoesNotBlockAnotherAdminStream(t *testing.T) {
	public := newChannelListener()
	_, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: public}}, []uint16{1})
	listener, _ := runtime.Listener(1)
	slowExternal := public.connect(t)
	slowRuntime, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slowExternal.Close() }()
	defer func() { _ = slowRuntime.Close() }()
	// Leave the first connection unread. Its per-stream flow-control window can
	// fill without holding the session reader or another stream.
	go func() { _, _ = slowExternal.Write(make([]byte, 512*1024)) }()

	fastExternal := public.connect(t)
	defer func() { _ = fastExternal.Close() }()
	fastRuntime, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fastRuntime.Close() }()
	_ = fastExternal.SetDeadline(time.Now().Add(2 * time.Second))
	_ = fastRuntime.SetDeadline(time.Now().Add(2 * time.Second))
	go func() { _, _ = fastExternal.Write([]byte("fast")) }()
	got := make([]byte, 4)
	if _, err = io.ReadFull(fastRuntime, got); err != nil || string(got) != "fast" {
		t.Fatalf("independent stream read = %q, %v", got, err)
	}
}

func TestGateBoundsUnclaimedConnections(t *testing.T) {
	public := newChannelListener()
	_, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: public}}, []uint16{1})
	listener, _ := runtime.Listener(1)
	clients := make([]net.Conn, 0, acceptDepth+1)
	for range acceptDepth + 1 {
		clients = append(clients, public.connect(t))
	}
	t.Cleanup(func() {
		for _, conn := range clients {
			_ = conn.Close()
		}
	})
	accepted := make([]net.Conn, 0, acceptDepth)
	for range acceptDepth {
		conn, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, conn)
	}
	for _, conn := range accepted {
		_ = conn.Close()
	}
	last := clients[len(clients)-1]
	_ = last.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := last.Read(make([]byte, 1)); err == nil {
		t.Fatal("connection beyond the unclaimed queue remained open")
	}
}

func TestGatePeerFailureAndShutdownWakeBlockedWork(t *testing.T) {
	public := newChannelListener()
	broker, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: public}}, []uint16{1})
	listener, _ := runtime.Listener(1)
	client := public.connect(t)
	relayed, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := relayed.Read(make([]byte, 1)); readDone <- err }()
	_ = broker.Close()
	select {
	case err = <-readDone:
		if err == nil {
			t.Fatal("blocked relay read succeeded after broker death")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("broker death did not wake relay read")
	}
	_ = client.Close()
	if _, err = listener.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after peer death = %v", err)
	}
}

func TestGateRejectsIdentityAndEndpointContracts(t *testing.T) {
	listener := newChannelListener()
	for name, endpoints := range map[string][]Endpoint{
		"none":      nil,
		"zero":      {{ID: 0, Listener: listener}},
		"nil":       {{ID: 1}},
		"duplicate": {{ID: 1, Listener: listener}, {ID: 1, Listener: listener}},
	} {
		t.Run(name, func(t *testing.T) {
			left, _ := net.Pipe()
			if _, err := NewBroker(context.Background(), left, testSession(t), endpoints); err == nil {
				t.Fatal("NewBroker accepted invalid endpoints")
			}
		})
	}

	left, right := net.Pipe()
	brokerSession, runtimeSession := testSession(t), testSession(t)
	result := make(chan error, 1)
	go func() {
		_, err := NewBroker(context.Background(), left, brokerSession, []Endpoint{{ID: 1, Listener: newChannelListener()}})
		result <- err
	}()
	_, runtimeErr := NewRuntime(context.Background(), right, runtimeSession, []uint16{1})
	brokerErr := <-result
	if !errors.Is(runtimeErr, protocol.ErrForeignResource) || !errors.Is(brokerErr, protocol.ErrForeignResource) {
		t.Fatalf("identity mismatch errors = %v, %v", brokerErr, runtimeErr)
	}
}

func TestRuntimeRejectsMalformedAndRuntimeOpenedStreams(t *testing.T) {
	public := newChannelListener()
	broker, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: public}}, []uint16{1})

	// A compromised R cannot create a broker-side request surface. N drains and
	// closes streams opened in the wrong direction.
	wrongDirection, err := runtime.session.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	_ = wrongDirection.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err = wrongDirection.Read(make([]byte, 1)); err == nil {
		t.Fatal("runtime-opened stream was not rejected")
	}

	// Exercise runtime fail-closed parsing independently from public accept.
	malformed, err := broker.session.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = malformed.Write([]byte("bad-head"))
	_ = malformed.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err = malformed.Read(make([]byte, 1)); err == nil {
		t.Fatal("malformed stream header was not rejected")
	}
}

func FuzzAdminGateHeaders(f *testing.F) {
	session := protocol.Session{ID: protocol.SessionID{1}, Epoch: 1, Version: protocol.Version, PolicyDigest: [32]byte{2}}
	hello := encodeHello(session)
	header := encodeStreamHeader(1)
	f.Add(append([]byte(nil), hello[:]...), append([]byte(nil), header[:]...))
	f.Add([]byte("bad"), []byte("bad"))
	f.Fuzz(func(t *testing.T, helloBytes, headerBytes []byte) {
		helloErr := validateHello(helloBytes, session)
		if helloErr == nil && string(helloBytes) != string(hello[:]) {
			t.Fatal("noncanonical hello accepted")
		}
		id, headerErr := decodeStreamHeader(headerBytes)
		if headerErr == nil {
			canonical := encodeStreamHeader(id)
			if id == 0 || string(canonical[:]) != string(headerBytes) {
				t.Fatal("noncanonical stream header accepted")
			}
		}
	})
}
