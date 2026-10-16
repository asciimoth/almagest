package isolated

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/almagest/lib/sandbox"
)

type Client struct {
	control  *wirePeer
	data     *wirePeer
	statusMu sync.Mutex
	status   sandbox.ServiceStatus
	once     sync.Once
	ownersMu sync.Mutex
	owners   map[string][]uint64
}

var _ sandbox.Service = (*Client)(nil)
var _ sandbox.OwnerBinder = (*Client)(nil)
var _ sandbox.OwnerRevoker = (*Client)(nil)
var _ sandbox.StatusReporter = (*Client)(nil)

func NewClient(ctx context.Context, control, data transport.Stream, session protocol.Session) (*Client, error) {
	client := &Client{owners: make(map[string][]uint64)}
	var err error
	client.control, err = newWirePeer(ctx, control, session, "control", nil)
	if err != nil {
		return nil, err
	}
	client.data, err = newWirePeer(ctx, data, session, "data", nil)
	if err != nil {
		_ = client.control.Close()
		return nil, err
	}
	var response serviceStatusResponse
	if err := client.control.call(ctx, opServiceStatus, empty{}, &response); err != nil {
		_ = client.Close()
		return nil, err
	}
	client.status = response.Value.Clone()
	return client, nil
}

func (c *Client) ServiceStatus() sandbox.ServiceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var response serviceStatusResponse
	if err := c.control.call(ctx, opServiceStatus, empty{}, &response); err == nil {
		c.statusMu.Lock()
		c.status = response.Value.Clone()
		c.statusMu.Unlock()
	}
	c.statusMu.Lock()
	defer c.statusMu.Unlock()
	return c.status.Clone()
}

func (c *Client) IssueAndBindOwner(ctx context.Context, owner sandbox.Owner) (sandbox.Owner, sandbox.OwnerSandboxes, error) {
	var response ownerResponse
	if err := c.control.call(ctx, opIssueBind, ownerRequest{Owner: owner}, &response); err != nil {
		return sandbox.Owner{}, nil, err
	}
	c.rememberOwner(response.Owner, response.ID)
	return response.Owner, &remoteOwners{client: c, id: response.ID}, nil
}

func (c *Client) BindOwner(ctx context.Context, owner sandbox.Owner) (sandbox.OwnerSandboxes, error) {
	var response ownerResponse
	if err := c.control.call(ctx, opBind, ownerRequest{Owner: owner}, &response); err != nil {
		return nil, err
	}
	c.rememberOwner(owner, response.ID)
	return &remoteOwners{client: c, id: response.ID}, nil
}

func (c *Client) RebindOwner(ctx context.Context, rename sandbox.OwnerRename) (sandbox.Owner, error) {
	var response ownerResponse
	err := c.control.call(ctx, opRebind, rebindRequest{Rename: rename}, &response)
	return response.Owner, err
}

func (c *Client) ReleaseOwner(ctx context.Context, owner sandbox.Owner) error {
	ids := c.ownerIDs(owner)
	err := c.control.call(ctx, opRelease, ownerRequest{Owner: owner, IDs: ids}, &empty{})
	if err != nil {
		c.restoreOwnerIDs(owner, ids)
	}
	return err
}

func (c *Client) RevokeOwner(ctx context.Context, owner sandbox.Owner) error {
	ids := c.ownerIDs(owner)
	err := c.control.call(ctx, opRevoke, ownerRequest{Owner: owner, IDs: ids}, &empty{})
	if err != nil {
		c.restoreOwnerIDs(owner, ids)
	}
	return err
}

func (c *Client) rememberOwner(owner sandbox.Owner, id uint64) {
	c.ownersMu.Lock()
	c.owners[owner.Key] = append(c.owners[owner.Key], id)
	c.ownersMu.Unlock()
}

func (c *Client) ownerIDs(owner sandbox.Owner) []uint64 {
	c.ownersMu.Lock()
	defer c.ownersMu.Unlock()
	ids := c.owners[owner.Key]
	delete(c.owners, owner.Key)
	return ids
}

func (c *Client) restoreOwnerIDs(owner sandbox.Owner, ids []uint64) {
	if len(ids) == 0 {
		return
	}
	c.ownersMu.Lock()
	c.owners[owner.Key] = append(c.owners[owner.Key], ids...)
	c.ownersMu.Unlock()
}

func (c *Client) Close() error {
	var result error
	c.once.Do(func() {
		if c.control != nil {
			result = errors.Join(result, c.control.Close())
		}
		if c.data != nil {
			result = errors.Join(result, c.data.Close())
		}
	})
	return result
}

type remoteOwners struct {
	client *Client
	id     uint64
}

func (o *remoteOwners) Acquire(ctx context.Context, slot string, template sandbox.TemplateID) (sandbox.Sandbox, error) {
	var response idResponse
	if err := o.client.control.call(ctx, opAcquire, acquireRequest{OwnerID: o.id, Slot: slot, Template: template}, &response); err != nil {
		return nil, err
	}
	instance := &remoteSandbox{client: o.client, id: response.ID, done: make(chan struct{})}
	go instance.wait()
	return instance, nil
}

func (o *remoteOwners) Remove(ctx context.Context, slot string) error {
	return o.client.control.call(ctx, opRemove, removeRequest{OwnerID: o.id, Slot: slot}, &empty{})
}

type remoteSandbox struct {
	client *Client
	id     uint64
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func (s *remoteSandbox) wait() {
	err := s.client.data.call(context.Background(), opSandboxWait, idRequest{ID: s.id}, &empty{})
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.once.Do(func() { close(s.done) })
}

func (s *remoteSandbox) Status() sandbox.Status {
	var response statusResponse
	if err := s.client.control.call(context.Background(), opSandboxStatus, idRequest{ID: s.id}, &response); err != nil {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
		return sandbox.Status{State: sandbox.Failed, Cleanup: encodeWireError(err)}
	}
	return response.Value.Clone()
}

func (s *remoteSandbox) FS(root sandbox.Root) (sandbox.Files, error) {
	if root != sandbox.Temporary && root != sandbox.Persistent {
		return nil, sandbox.ErrInvalid
	}
	return &remoteFiles{sandbox: s, root: root}, nil
}

func (s *remoteSandbox) ChildPath(path sandbox.Path) (string, error) {
	var response stringResponse
	err := s.client.control.call(context.Background(), opChildPath, pathRequest{SandboxID: s.id, Path: path}, &response)
	return response.Value, err
}

func (s *remoteSandbox) ProgramPath(program sandbox.ProgramID) (string, error) {
	var response stringResponse
	err := s.client.control.call(context.Background(), opProgramPath, programRequest{SandboxID: s.id, Program: program}, &response)
	return response.Value, err
}

func (s *remoteSandbox) ProcessContext() sandbox.ProcessContext {
	var response processResponse
	if err := s.client.control.call(context.Background(), opProcess, idRequest{ID: s.id}, &response); err != nil {
		s.mu.Lock()
		s.err = err
		s.mu.Unlock()
	}
	return response.Value
}

func (s *remoteSandbox) NewCommand(program sandbox.ProgramID, launch sandbox.Launch) (sandbox.Command, error) {
	data, err := json.Marshal(launch)
	if err != nil || len(data) == 0 {
		return nil, sandbox.ErrInvalid
	}
	if len(data) > maxCommandLaunchBytes {
		return nil, sandbox.ErrQuota
	}
	// Small launches use one control request. Large values use the data lane so
	// every frame remains bounded by the common transport limit.
	if len(data) > maxDataChunk {
		var upload idResponse
		request := commandUploadRequest{SandboxID: s.id, Program: program, Size: uint32(len(data)), Digest: sha256.Sum256(data)}
		if err := s.client.control.call(context.Background(), opCommandUpload, request, &upload); err != nil {
			return nil, err
		}
		for offset := 0; offset < len(data); {
			chunk := data[offset:min(len(data), offset+maxDataChunk)]
			if err := s.client.data.call(context.Background(), opCommandChunk, commandChunkRequest{UploadID: upload.ID, Offset: uint32(offset), Data: chunk}, &empty{}); err != nil {
				_ = s.client.control.call(context.Background(), opCommandAbort, idRequest(upload), &empty{})
				return nil, err
			}
			offset += len(chunk)
		}
		var response idResponse
		if err := s.client.control.call(context.Background(), opCommandCommit, idRequest(upload), &response); err != nil {
			return nil, err
		}
		return &remoteCommand{client: s.client, id: response.ID, done: make(chan struct{})}, nil
	}
	var response idResponse
	if err := s.client.control.call(context.Background(), opNewCommand, newCommandRequest{SandboxID: s.id, Program: program, Launch: launch}, &response); err != nil {
		return nil, err
	}
	return &remoteCommand{client: s.client, id: response.ID, done: make(chan struct{})}, nil
}

func (s *remoteSandbox) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var response networkResponse
	if err := s.client.control.call(ctx, opDial, networkRequest{SandboxID: s.id, Network: network, Address: address}, &response); err != nil {
		return nil, err
	}
	return newRemoteConn(s.client, response), nil
}

func (s *remoteSandbox) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	var response networkResponse
	if err := s.client.control.call(ctx, opListen, networkRequest{SandboxID: s.id, Network: network, Address: address}, &response); err != nil {
		return nil, err
	}
	return &remoteListener{client: s.client, id: response.ID, address: remoteAddr{value: response.Local}}, nil
}

func (s *remoteSandbox) ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error) {
	var response networkResponse
	if err := s.client.control.call(ctx, opListenPacket, networkRequest{SandboxID: s.id, Network: network, Address: address}, &response); err != nil {
		return nil, err
	}
	return &remotePacket{client: s.client, id: response.ID, local: remoteAddr{value: response.Local}}, nil
}

func (s *remoteSandbox) DialUnix(ctx context.Context, path sandbox.Path) (net.Conn, error) {
	var response networkResponse
	if err := s.client.control.call(ctx, opDialUnix, networkRequest{SandboxID: s.id, Path: path}, &response); err != nil {
		return nil, err
	}
	return newRemoteConn(s.client, response), nil
}

func (s *remoteSandbox) ListenUnix(ctx context.Context, path sandbox.Path) (net.Listener, error) {
	var response networkResponse
	if err := s.client.control.call(ctx, opListenUnix, networkRequest{SandboxID: s.id, Path: path}, &response); err != nil {
		return nil, err
	}
	return &remoteListener{client: s.client, id: response.ID, address: remoteAddr{value: response.Local}}, nil
}

func (s *remoteSandbox) Done() <-chan struct{} { return s.done }
func (s *remoteSandbox) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *remoteSandbox) Close() error {
	err := s.client.control.call(context.Background(), opSandboxClose, idRequest{ID: s.id}, &empty{})
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		err = errors.Join(err, sandbox.ErrUnavailable)
	}
	return err
}

type remoteCommand struct {
	client  *Client
	id      uint64
	done    chan struct{}
	mu      sync.Mutex
	started bool
	exit    sandbox.Exit
	err     error
}

func (c *remoteCommand) pipe(kind string) (*remoteStream, error) {
	var response idResponse
	if err := c.client.control.call(context.Background(), opCommandPipe, commandPipeRequest{CommandID: c.id, Kind: kind}, &response); err != nil {
		return nil, err
	}
	return &remoteStream{client: c.client, id: response.ID, readable: kind != "stdin", writable: kind == "stdin"}, nil
}
func (c *remoteCommand) StdinPipe() (io.WriteCloser, error) { return c.pipe("stdin") }
func (c *remoteCommand) StdoutPipe() (io.ReadCloser, error) { return c.pipe("stdout") }
func (c *remoteCommand) StderrPipe() (io.ReadCloser, error) { return c.pipe("stderr") }

func (c *remoteCommand) Start(ctx context.Context) error {
	if ctx == nil {
		return sandbox.ErrInvalid
	}
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return sandbox.ErrConflict
	}
	c.started = true
	c.mu.Unlock()
	if err := c.client.control.call(ctx, opCommandStart, commandStartRequest{CommandID: c.id}, &empty{}); err != nil {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
		return err
	}
	go c.wait()
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Kill()
		case <-c.done:
		}
	}()
	return nil
}

func (c *remoteCommand) wait() {
	var response commandWaitResponse
	err := c.client.data.call(context.Background(), opCommandWait, idRequest{ID: c.id}, &response)
	if err == nil && response.ExitError {
		err = &sandbox.ExitError{Exit: response.Exit}
	}
	c.mu.Lock()
	c.exit, c.err = response.Exit, err
	c.mu.Unlock()
	close(c.done)
}

func (c *remoteCommand) Wait() (sandbox.Exit, error) {
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if !started {
		return sandbox.Exit{}, sandbox.ErrConflict
	}
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit, c.err
}
func (c *remoteCommand) Signal(signal sandbox.Signal) error {
	return c.client.control.call(context.Background(), opCommandSignal, commandSignalRequest{CommandID: c.id, Signal: signal}, &empty{})
}
func (c *remoteCommand) Kill() error {
	return c.client.control.call(context.Background(), opCommandKill, idRequest{ID: c.id}, &empty{})
}
func (c *remoteCommand) Done() <-chan struct{} { return c.done }

type remoteFiles struct {
	sandbox *remoteSandbox
	root    sandbox.Root
}

func (f *remoteFiles) request(ctx context.Context, request fsRequest) (fsResponse, error) {
	request.SandboxID, request.Root = f.sandbox.id, f.root
	var response fsResponse
	err := f.sandbox.client.control.call(ctx, opFS, request, &response)
	return response, err
}
func (f *remoteFiles) MkdirAll(ctx context.Context, name string, mode fs.FileMode) error {
	_, err := f.request(ctx, fsRequest{Action: "mkdir_all", Name: name, Mode: uint32(mode)})
	return err
}
func (f *remoteFiles) TempDir(ctx context.Context, directory, pattern string) (string, error) {
	response, err := f.request(ctx, fsRequest{Action: "temp_dir", Name: directory, Pattern: pattern})
	return response.Name, err
}
func (f *remoteFiles) Open(ctx context.Context, name string, options sandbox.OpenOptions) (sandbox.File, error) {
	var response idResponse
	err := f.sandbox.client.control.call(ctx, opFileOpen, fileOpenRequest{SandboxID: f.sandbox.id, Root: f.root, Name: name, Options: options}, &response)
	if err != nil {
		return nil, err
	}
	return &remoteFile{client: f.sandbox.client, id: response.ID}, nil
}
func (f *remoteFiles) ReadFile(ctx context.Context, name string, maximum int64) ([]byte, error) {
	if maximum < 0 {
		return nil, sandbox.ErrInvalid
	}
	file, err := f.Open(ctx, name, sandbox.OpenOptions{Read: true})
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	reader := io.LimitReader(file, maximum+1)
	data, err := io.ReadAll(reader)
	if err == nil && int64(len(data)) > maximum {
		err = sandbox.ErrQuota
		data = data[:maximum]
	}
	return data, err
}
func (f *remoteFiles) WriteFile(ctx context.Context, name string, data []byte, mode fs.FileMode) error {
	file, err := f.Open(ctx, name, sandbox.OpenOptions{Write: true, Create: true, Truncate: true, Mode: mode})
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}
func (f *remoteFiles) Rename(ctx context.Context, oldName, newName string) error {
	_, err := f.request(ctx, fsRequest{Action: "rename", Name: oldName, Other: newName})
	return err
}
func (f *remoteFiles) Stat(ctx context.Context, name string) (fs.FileInfo, error) {
	response, err := f.request(ctx, fsRequest{Action: "stat", Name: name})
	return remoteFileInfo{value: response.Info}, err
}
func (f *remoteFiles) ReadDir(ctx context.Context, name string, maximum int) ([]fs.DirEntry, error) {
	response, err := f.request(ctx, fsRequest{Action: "read_dir", Name: name, Maximum: int64(maximum)})
	if err != nil {
		return nil, err
	}
	entries := make([]fs.DirEntry, 0, len(response.Entries))
	for _, entry := range response.Entries {
		entries = append(entries, remoteDirEntry{value: entry})
	}
	return entries, nil
}
func (f *remoteFiles) Remove(ctx context.Context, name string) error {
	_, err := f.request(ctx, fsRequest{Action: "remove", Name: name})
	return err
}
func (f *remoteFiles) RemoveAll(ctx context.Context, name string) error {
	_, err := f.request(ctx, fsRequest{Action: "remove_all", Name: name})
	return err
}

type remoteFile struct {
	client *Client
	id     uint64
}

func (f *remoteFile) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	var response dataResponse
	err := f.client.data.call(context.Background(), opFileRead, fileIORequest{ID: f.id, Count: min(len(data), maxDataChunk)}, &response)
	copy(data, response.Data)
	return response.Count, err
}
func (f *remoteFile) Write(data []byte) (int, error) {
	total := 0
	for len(data) != 0 {
		chunk := data[:min(len(data), maxDataChunk)]
		var response dataResponse
		err := f.client.data.call(context.Background(), opFileWrite, fileIORequest{ID: f.id, Data: chunk}, &response)
		total += response.Count
		data = data[response.Count:]
		if err != nil {
			return total, err
		}
		if response.Count == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}
func (f *remoteFile) Seek(offset int64, whence int) (int64, error) {
	var response seekResponse
	err := f.client.data.call(context.Background(), opFileSeek, fileIORequest{ID: f.id, Offset: offset, Whence: whence}, &response)
	return response.Offset, err
}
func (f *remoteFile) Close() error {
	return f.client.control.call(context.Background(), opFileClose, idRequest{ID: f.id}, &empty{})
}
func (f *remoteFile) Stat() (fs.FileInfo, error) {
	var response fsResponse
	err := f.client.control.call(context.Background(), opFileStat, idRequest{ID: f.id}, &response)
	return remoteFileInfo{value: response.Info}, err
}
func (f *remoteFile) Sync() error {
	return f.client.control.call(context.Background(), opFileSync, idRequest{ID: f.id}, &empty{})
}

type remoteFileInfo struct{ value wireFileInfo }

func (i remoteFileInfo) Name() string       { return i.value.Name }
func (i remoteFileInfo) Size() int64        { return i.value.Size }
func (i remoteFileInfo) Mode() fs.FileMode  { return fs.FileMode(i.value.Mode) }
func (i remoteFileInfo) ModTime() time.Time { return time.Unix(0, i.value.ModUnix) }
func (i remoteFileInfo) IsDir() bool        { return i.value.Dir }
func (i remoteFileInfo) Sys() any           { return nil }

type remoteDirEntry struct{ value wireDirEntry }

func (e remoteDirEntry) Name() string               { return e.value.Name }
func (e remoteDirEntry) IsDir() bool                { return e.value.Info.Dir }
func (e remoteDirEntry) Type() fs.FileMode          { return fs.FileMode(e.value.Type) }
func (e remoteDirEntry) Info() (fs.FileInfo, error) { return remoteFileInfo{value: e.value.Info}, nil }

type remoteStream struct {
	client             *Client
	id                 uint64
	readable, writable bool
	once               sync.Once
}

func (s *remoteStream) Read(data []byte) (int, error) {
	if !s.readable || len(data) == 0 {
		if !s.readable {
			return 0, sandbox.ErrDenied
		}
		return 0, nil
	}
	var response dataResponse
	err := s.client.data.call(context.Background(), opStreamRead, fileIORequest{ID: s.id, Count: min(len(data), maxDataChunk)}, &response)
	copy(data, response.Data)
	return response.Count, err
}
func (s *remoteStream) Write(data []byte) (int, error) {
	if !s.writable {
		return 0, sandbox.ErrDenied
	}
	total := 0
	for len(data) != 0 {
		chunk := data[:min(len(data), maxDataChunk)]
		var response dataResponse
		err := s.client.data.call(context.Background(), opStreamWrite, fileIORequest{ID: s.id, Data: chunk}, &response)
		total += response.Count
		data = data[response.Count:]
		if err != nil {
			return total, err
		}
		if response.Count == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}
func (s *remoteStream) Close() (err error) {
	s.once.Do(func() {
		err = s.client.control.call(context.Background(), opStreamClose, idRequest{ID: s.id}, &empty{})
	})
	return err
}

type remoteAddr struct{ value wireAddr }

func (a remoteAddr) Network() string { return a.value.Network }
func (a remoteAddr) String() string  { return a.value.Value }

type remoteConn struct {
	client        *Client
	id            uint64
	local, remote net.Addr
}

func newRemoteConn(client *Client, response networkResponse) *remoteConn {
	return &remoteConn{client: client, id: response.ID, local: remoteAddr{value: response.Local}, remote: remoteAddr{value: response.Remote}}
}
func (c *remoteConn) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	var response dataResponse
	err := c.client.data.call(context.Background(), opConnRead, fileIORequest{ID: c.id, Count: min(len(data), maxDataChunk)}, &response)
	copy(data, response.Data)
	return response.Count, err
}
func (c *remoteConn) Write(data []byte) (int, error) {
	return writeChunks(c.client, opConnWrite, c.id, data)
}
func (c *remoteConn) Close() error {
	return c.client.control.call(context.Background(), opConnClose, idRequest{ID: c.id}, &empty{})
}
func (c *remoteConn) LocalAddr() net.Addr                { return c.local }
func (c *remoteConn) RemoteAddr() net.Addr               { return c.remote }
func (c *remoteConn) SetDeadline(t time.Time) error      { return c.deadline("all", t) }
func (c *remoteConn) SetReadDeadline(t time.Time) error  { return c.deadline("read", t) }
func (c *remoteConn) SetWriteDeadline(t time.Time) error { return c.deadline("write", t) }
func (c *remoteConn) deadline(kind string, value time.Time) error {
	return c.client.control.call(context.Background(), opConnDeadline, deadlineRequest{ID: c.id, Kind: kind, UnixNano: timeWire(value)}, &empty{})
}

type remoteListener struct {
	client  *Client
	id      uint64
	address net.Addr
}

func (l *remoteListener) Accept() (net.Conn, error) {
	var response networkResponse
	if err := l.client.data.call(context.Background(), opAccept, idRequest{ID: l.id}, &response); err != nil {
		return nil, err
	}
	return newRemoteConn(l.client, response), nil
}
func (l *remoteListener) Close() error {
	return l.client.control.call(context.Background(), opListenerClose, idRequest{ID: l.id}, &empty{})
}
func (l *remoteListener) Addr() net.Addr { return l.address }

type remotePacket struct {
	client *Client
	id     uint64
	local  net.Addr
}

func (p *remotePacket) ReadFrom(data []byte) (int, net.Addr, error) {
	if len(data) == 0 {
		return 0, nil, nil
	}
	var response packetReadResponse
	err := p.client.data.call(context.Background(), opPacketRead, fileIORequest{ID: p.id, Count: min(len(data), maxDataChunk)}, &response)
	copy(data, response.Data)
	return len(response.Data), remoteAddr{value: response.Addr}, err
}
func (p *remotePacket) WriteTo(data []byte, address net.Addr) (int, error) {
	if len(data) > maxDataChunk {
		return 0, sandbox.ErrQuota
	}
	var response dataResponse
	err := p.client.data.call(context.Background(), opPacketWrite, packetWriteRequest{ID: p.id, Data: data, Addr: makeWireAddr(address)}, &response)
	return response.Count, err
}
func (p *remotePacket) Close() error {
	return p.client.control.call(context.Background(), opPacketClose, idRequest{ID: p.id}, &empty{})
}
func (p *remotePacket) LocalAddr() net.Addr                { return p.local }
func (p *remotePacket) SetDeadline(t time.Time) error      { return p.deadline("all", t) }
func (p *remotePacket) SetReadDeadline(t time.Time) error  { return p.deadline("read", t) }
func (p *remotePacket) SetWriteDeadline(t time.Time) error { return p.deadline("write", t) }
func (p *remotePacket) deadline(kind string, value time.Time) error {
	return p.client.control.call(context.Background(), opPacketDeadline, deadlineRequest{ID: p.id, Kind: kind, UnixNano: timeWire(value)}, &empty{})
}

func writeChunks(client *Client, operation string, id uint64, data []byte) (int, error) {
	total := 0
	for len(data) != 0 {
		chunk := data[:min(len(data), maxDataChunk)]
		var response dataResponse
		err := client.data.call(context.Background(), operation, fileIORequest{ID: id, Data: chunk}, &response)
		total += response.Count
		data = data[response.Count:]
		if err != nil {
			return total, err
		}
		if response.Count == 0 {
			return total, io.ErrNoProgress
		}
	}
	return total, nil
}

func timeWire(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixNano()
}
