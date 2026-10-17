// Package isolated implements the private runtime-to-sandbox-supervisor
// protocol. Protected paths and launch policy stay in the supervisor process.
package isolated

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	sandbox "github.com/asciimoth/gobox"
)

const (
	wireVersion       = 2
	maxWireConcurrent = 128
	maxDataChunk      = 32 * 1024
	// A launch has at most 128 inputs, and a sandbox value has a maximum
	// encoded size of about 64 KiB. Keep some space for JSON field names.
	maxCommandLaunchBytes = 10 * 1024 * 1024
)

type wireHello struct {
	Version uint32           `json:"version"`
	Session protocol.Session `json:"session"`
	Lane    string           `json:"lane"`
}

type wireMessage struct {
	Kind   string          `json:"kind"`
	ID     uint64          `json:"id"`
	Op     string          `json:"op,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
	Detail string          `json:"detail,omitempty"`
}

type wireHandler func(context.Context, string, json.RawMessage) (any, error)

type wirePeer struct {
	stream  transport.Stream
	session protocol.Session
	handler wireHandler
	ctx     context.Context
	cancel  context.CancelFunc

	writeMu sync.Mutex
	mu      sync.Mutex
	next    uint64
	err     error
	pending map[uint64]chan wireMessage
	active  map[uint64]context.CancelFunc
	sem     chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newWirePeer(ctx context.Context, stream transport.Stream, session protocol.Session, lane string, handler wireHandler) (*wirePeer, error) {
	if ctx == nil || stream == nil || session.Validate() != nil || lane != "control" && lane != "data" {
		if stream != nil {
			_ = stream.Close()
		}
		return nil, protocol.ErrInvalidContract
	}
	deadline := time.Now().Add(5 * time.Second)
	hello := wireHello{Version: wireVersion, Session: session, Lane: lane}
	written := make(chan error, 1)
	go func() { written <- transport.WriteFrameDeadline(stream, hello, deadline) }()
	var remote wireHello
	err := transport.ReadFrameDeadline(stream, &remote, deadline)
	writeErr := <-written
	if err == nil {
		err = writeErr
	}
	if err == nil && remote != hello {
		err = protocol.ErrInvalidContract
	}
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &wirePeer{stream: stream, session: session, handler: handler, ctx: pctx, cancel: cancel,
		pending: make(map[uint64]chan wireMessage), active: make(map[uint64]context.CancelFunc),
		sem: make(chan struct{}, maxWireConcurrent), done: make(chan struct{})}
	go p.readLoop()
	return p, nil
}

func (p *wirePeer) Close() error {
	p.fail(net.ErrClosed)
	<-p.done
	return nil
}

func (p *wirePeer) Done() <-chan struct{} { return p.ctx.Done() }

func (p *wirePeer) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return p.ctx.Err()
}

func (p *wirePeer) fail(err error) {
	p.once.Do(func() {
		p.mu.Lock()
		p.err = err
		for _, cancel := range p.active {
			cancel()
		}
		p.mu.Unlock()
		p.cancel()
		_ = p.stream.Close()
	})
}

func (p *wirePeer) write(message wireMessage) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := p.ctx.Err(); err != nil {
		return p.Err()
	}
	if err := transport.WriteFrame(p.stream, message); err != nil {
		p.fail(err)
		return err
	}
	return nil
}

func (p *wirePeer) call(ctx context.Context, op string, request, response any) error {
	if ctx == nil || !validOperation(op) {
		return sandbox.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return sandbox.ErrInvalid
	}
	p.mu.Lock()
	if p.err != nil {
		err = p.err
		p.mu.Unlock()
		return err
	}
	if len(p.pending) >= maxWireConcurrent || p.next == ^uint64(0) {
		p.mu.Unlock()
		return sandbox.ErrQuota
	}
	p.next++
	id := p.next
	reply := make(chan wireMessage, 1)
	p.pending[id] = reply
	p.mu.Unlock()
	if err := p.write(wireMessage{Kind: "request", ID: id, Op: op, Body: body}); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return err
	}
	select {
	case message := <-reply:
		if message.Error != "" {
			base := decodeWireError(message.Error)
			if message.Detail != "" {
				return errors.Join(base, errors.New(message.Detail))
			}
			return base
		}
		if response == nil {
			return nil
		}
		return transport.Decode(message.Body, response)
	case <-ctx.Done():
		_ = p.write(wireMessage{Kind: "cancel", ID: id})
		return ctx.Err()
	case <-p.ctx.Done():
		return p.Err()
	}
}

func (p *wirePeer) readLoop() {
	defer close(p.done)
	for {
		var message wireMessage
		if err := transport.ReadFrame(p.stream, &message); err != nil {
			p.fail(err)
			return
		}
		if err := p.receive(message); err != nil {
			p.fail(err)
			return
		}
	}
}

func (p *wirePeer) receive(message wireMessage) error {
	if message.ID == 0 {
		return protocol.ErrInvalidContract
	}
	switch message.Kind {
	case "reply":
		if message.Op != "" || (message.Error == "") == (len(message.Body) == 0) || message.Detail != "" && (message.Error == "" || !validPublicError(message.Detail)) {
			return protocol.ErrInvalidContract
		}
		p.mu.Lock()
		ch := p.pending[message.ID]
		delete(p.pending, message.ID)
		p.mu.Unlock()
		if ch == nil {
			return protocol.ErrInvalidContract
		}
		ch <- message
	case "cancel":
		if message.Op != "" || message.Error != "" || message.Detail != "" || len(message.Body) != 0 {
			return protocol.ErrInvalidContract
		}
		p.mu.Lock()
		cancel := p.active[message.ID]
		p.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case "request":
		if !validOperation(message.Op) || message.Error != "" || message.Detail != "" || len(message.Body) == 0 || p.handler == nil {
			return protocol.ErrInvalidContract
		}
		select {
		case p.sem <- struct{}{}:
		default:
			return p.write(wireMessage{Kind: "reply", ID: message.ID, Error: encodeWireError(sandbox.ErrQuota)})
		}
		ctx, cancel := context.WithCancel(p.ctx)
		p.mu.Lock()
		if p.active[message.ID] != nil {
			p.mu.Unlock()
			cancel()
			<-p.sem
			return protocol.ErrInvalidContract
		}
		p.active[message.ID] = cancel
		p.mu.Unlock()
		go p.handle(ctx, message)
	default:
		return protocol.ErrInvalidContract
	}
	return nil
}

func (p *wirePeer) handle(ctx context.Context, message wireMessage) {
	value, err := p.handler(ctx, message.Op, message.Body)
	reply := wireMessage{Kind: "reply", ID: message.ID, Error: encodeWireError(err), Detail: publicErrorDetail(err)}
	if err == nil {
		reply.Body, err = json.Marshal(value)
		if err != nil {
			reply.Body = nil
			reply.Error = encodeWireError(sandbox.ErrInvalid)
		}
	}
	p.mu.Lock()
	if cancel := p.active[message.ID]; cancel != nil {
		cancel()
	}
	delete(p.active, message.ID)
	p.mu.Unlock()
	<-p.sem
	if err := p.write(reply); err != nil {
		p.fail(err)
	}
}

func publicErrorDetail(err error) string {
	var value interface{ PublicError() string }
	if !errors.As(err, &value) {
		return ""
	}
	detail := value.PublicError()
	if !validPublicError(detail) {
		return ""
	}
	return detail
}

func validPublicError(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

var wireErrors = map[string]error{
	"denied": sandbox.ErrDenied, "unsupported": sandbox.ErrUnsupported,
	"unavailable": sandbox.ErrUnavailable, "closed": sandbox.ErrClosed,
	"stale": sandbox.ErrStale, "conflict": sandbox.ErrConflict,
	"quota": sandbox.ErrQuota, "launch": sandbox.ErrLaunch, "invalid": sandbox.ErrInvalid,
	"canceled": context.Canceled, "deadline": context.DeadlineExceeded,
	"eof": io.EOF, "not_exist": fs.ErrNotExist, "exist": fs.ErrExist,
	"permission": fs.ErrPermission, "net_closed": net.ErrClosed,
	"file_closed": os.ErrClosed, "process_done": os.ErrProcessDone,
	"timeout": os.ErrDeadlineExceeded, "internal": sandbox.ErrUnavailable,
}

func encodeWireError(err error) string {
	if err == nil {
		return ""
	}
	for code, target := range wireErrors {
		if code != "internal" && errors.Is(err, target) {
			return code
		}
	}
	return "internal"
}

func decodeWireError(code string) error {
	if err := wireErrors[code]; err != nil {
		return err
	}
	return protocol.ErrInvalidContract
}

func decodeBody(body json.RawMessage, value any) error {
	if err := transport.Decode(body, value); err != nil {
		return sandbox.ErrInvalid
	}
	return nil
}
