package isolated

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net"
	"sync"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/almagest/lib/sandbox"
)

type streamResource struct {
	reader io.Reader
	writer io.Writer
	closer io.Closer
}

type commandUpload struct {
	sandbox sandbox.Sandbox
	program sandbox.ProgramID
	size    uint32
	digest  [32]byte
	data    []byte
}

type Server struct {
	service sandbox.Service
	control *wirePeer
	data    *wirePeer

	mu        sync.Mutex
	next      uint64
	owners    map[uint64]sandbox.OwnerSandboxes
	sandboxes map[uint64]sandbox.Sandbox
	commands  map[uint64]sandbox.Command
	uploads   map[uint64]*commandUpload
	streams   map[uint64]streamResource
	files     map[uint64]sandbox.File
	conns     map[uint64]net.Conn
	listeners map[uint64]net.Listener
	packets   map[uint64]net.PacketConn
	closed    bool
	closeDone chan struct{}
	closeErr  error
}

func NewServer(ctx context.Context, control, data transport.Stream, session protocol.Session, service sandbox.Service) (*Server, error) {
	if service == nil {
		return nil, sandbox.ErrInvalid
	}
	s := &Server{service: service, owners: make(map[uint64]sandbox.OwnerSandboxes), sandboxes: make(map[uint64]sandbox.Sandbox),
		commands: make(map[uint64]sandbox.Command), uploads: make(map[uint64]*commandUpload), streams: make(map[uint64]streamResource), files: make(map[uint64]sandbox.File),
		conns: make(map[uint64]net.Conn), listeners: make(map[uint64]net.Listener), packets: make(map[uint64]net.PacketConn), closeDone: make(chan struct{})}
	var err error
	s.control, err = newWirePeer(ctx, control, session, "control", s.handleControl)
	if err != nil {
		return nil, err
	}
	s.data, err = newWirePeer(ctx, data, session, "data", s.handleData)
	if err != nil {
		_ = s.control.Close()
		return nil, err
	}
	go func() {
		select {
		case <-s.control.Done():
		case <-s.data.Done():
		case <-ctx.Done():
		}
		_ = s.Close()
	}()
	return s, nil
}

func (s *Server) Done() <-chan struct{} { return s.control.Done() }

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return s.closeErr
	}
	s.closed = true
	commands, streams, files := maps.Clone(s.commands), maps.Clone(s.streams), maps.Clone(s.files)
	conns, listeners, packets := maps.Clone(s.conns), maps.Clone(s.listeners), maps.Clone(s.packets)
	s.mu.Unlock()
	var result error
	for _, command := range commands {
		result = errors.Join(result, command.Kill())
	}
	for _, stream := range streams {
		result = errors.Join(result, stream.closer.Close())
	}
	for _, file := range files {
		result = errors.Join(result, file.Close())
	}
	for _, connection := range conns {
		result = errors.Join(result, connection.Close())
	}
	for _, listener := range listeners {
		result = errors.Join(result, listener.Close())
	}
	for _, packet := range packets {
		result = errors.Join(result, packet.Close())
	}
	result = errors.Join(result, s.service.Close())
	if s.control != nil {
		result = errors.Join(result, s.control.Close())
	}
	if s.data != nil {
		result = errors.Join(result, s.data.Close())
	}
	s.mu.Lock()
	s.closeErr = result
	close(s.closeDone)
	s.mu.Unlock()
	return result
}

func (s *Server) allocate(value any) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	live := len(s.owners) + len(s.sandboxes) + len(s.commands) + len(s.uploads) + len(s.streams) + len(s.files) + len(s.conns) + len(s.listeners) + len(s.packets)
	if s.closed || s.next == ^uint64(0) || live >= 65536 {
		return 0, sandbox.ErrQuota
	}
	s.next++
	id := s.next
	switch item := value.(type) {
	case sandbox.OwnerSandboxes:
		s.owners[id] = item
	case sandbox.Sandbox:
		s.sandboxes[id] = item
	case sandbox.Command:
		s.commands[id] = item
	case *commandUpload:
		s.uploads[id] = item
	case streamResource:
		s.streams[id] = item
	case sandbox.File:
		s.files[id] = item
	case net.Conn:
		s.conns[id] = item
	case net.Listener:
		s.listeners[id] = item
	case net.PacketConn:
		s.packets[id] = item
	default:
		return 0, sandbox.ErrInvalid
	}
	return id, nil
}

func (s *Server) handleControl(ctx context.Context, operation string, body json.RawMessage) (any, error) {
	switch operation {
	case opServiceStatus:
		reporter, ok := s.service.(sandbox.StatusReporter)
		if !ok {
			return nil, sandbox.ErrUnavailable
		}
		return serviceStatusResponse{Value: reporter.ServiceStatus()}, nil
	case opIssueBind, opBind:
		var request ownerRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		owner := request.Owner
		var bound sandbox.OwnerSandboxes
		var err error
		if operation == opIssueBind {
			binder, ok := s.service.(sandbox.OwnerBinder)
			if !ok {
				return nil, sandbox.ErrUnavailable
			}
			owner, bound, err = binder.IssueAndBindOwner(ctx, owner)
		} else {
			bound, err = s.service.BindOwner(ctx, owner)
		}
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(bound)
		if err != nil {
			return nil, err
		}
		return ownerResponse{Owner: owner, ID: id}, nil
	case opRebind:
		var request rebindRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		owner, err := s.service.RebindOwner(ctx, request.Rename)
		return ownerResponse{Owner: owner}, err
	case opRelease, opRevoke:
		var request ownerRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		if operation == opRevoke {
			revoker, ok := s.service.(sandbox.OwnerRevoker)
			if !ok {
				return nil, sandbox.ErrUnavailable
			}
			if err := revoker.RevokeOwner(ctx, request.Owner); err != nil {
				return nil, err
			}
		} else if err := s.service.ReleaseOwner(ctx, request.Owner); err != nil {
			return nil, err
		}
		s.mu.Lock()
		for _, id := range request.IDs {
			delete(s.owners, id)
		}
		s.mu.Unlock()
		return empty{}, nil
	case opAcquire:
		var request acquireRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		owner := s.owners[request.OwnerID]
		s.mu.Unlock()
		if owner == nil {
			return nil, sandbox.ErrStale
		}
		instance, err := owner.Acquire(ctx, request.Slot, request.Template)
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(instance)
		return idResponse{ID: id}, err
	case opRemove:
		var request removeRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		owner := s.owners[request.OwnerID]
		s.mu.Unlock()
		if owner == nil {
			return nil, sandbox.ErrStale
		}
		return empty{}, owner.Remove(ctx, request.Slot)
	case opSandboxStatus, opSandboxClose, opProcess:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		instance := s.sandboxes[request.ID]
		s.mu.Unlock()
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		switch operation {
		case opSandboxStatus:
			return statusResponse{Value: instance.Status()}, nil
		case opProcess:
			return processResponse{Value: instance.ProcessContext()}, nil
		default:
			return empty{}, instance.Close()
		}
	case opChildPath:
		var request pathRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		instance := s.sandbox(request.SandboxID)
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		value, err := instance.ChildPath(request.Path)
		return stringResponse{Value: value}, err
	case opProgramPath:
		var request programRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		instance := s.sandbox(request.SandboxID)
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		value, err := instance.ProgramPath(request.Program)
		return stringResponse{Value: value}, err
	case opNewCommand:
		var request newCommandRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		instance := s.sandbox(request.SandboxID)
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		command, err := instance.NewCommand(request.Program, request.Launch)
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(command)
		return idResponse{ID: id}, err
	case opCommandUpload:
		var request commandUploadRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		instance := s.sandbox(request.SandboxID)
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		if request.Size == 0 || request.Size > maxCommandLaunchBytes {
			return nil, sandbox.ErrQuota
		}
		id, err := s.allocate(&commandUpload{sandbox: instance, program: request.Program, size: request.Size, digest: request.Digest, data: make([]byte, 0, request.Size)})
		return idResponse{ID: id}, err
	case opCommandCommit:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		upload := s.uploads[request.ID]
		delete(s.uploads, request.ID)
		s.mu.Unlock()
		if upload == nil {
			return nil, sandbox.ErrStale
		}
		if len(upload.data) != int(upload.size) || sha256.Sum256(upload.data) != upload.digest {
			return nil, sandbox.ErrInvalid
		}
		var launch sandbox.Launch
		decoder := json.NewDecoder(bytes.NewReader(upload.data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&launch); err != nil {
			return nil, sandbox.ErrInvalid
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return nil, sandbox.ErrInvalid
		}
		command, err := upload.sandbox.NewCommand(upload.program, launch)
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(command)
		return idResponse{ID: id}, err
	case opCommandAbort:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		_, ok := s.uploads[request.ID]
		delete(s.uploads, request.ID)
		s.mu.Unlock()
		if !ok {
			return nil, sandbox.ErrStale
		}
		return empty{}, nil
	case opCommandPipe:
		var request commandPipeRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		command := s.command(request.CommandID)
		if command == nil {
			return nil, sandbox.ErrStale
		}
		var stream streamResource
		switch request.Kind {
		case "stdin":
			pipe, err := command.StdinPipe()
			if err != nil {
				return nil, err
			}
			stream = streamResource{writer: pipe, closer: pipe}
		case "stdout", "stderr":
			var pipe io.ReadCloser
			var err error
			if request.Kind == "stdout" {
				pipe, err = command.StdoutPipe()
			} else {
				pipe, err = command.StderrPipe()
			}
			if err != nil {
				return nil, err
			}
			stream = streamResource{reader: pipe, closer: pipe}
		default:
			return nil, sandbox.ErrInvalid
		}
		id, err := s.allocate(stream)
		return idResponse{ID: id}, err
	case opCommandStart:
		var request commandStartRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		command := s.command(request.CommandID)
		if command == nil {
			return nil, sandbox.ErrStale
		}
		// The command resource owns its lifetime. The request context ends when
		// this reply is sent; client cancellation is relayed as Kill.
		return empty{}, command.Start(context.Background())
	case opCommandSignal:
		var request commandSignalRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		command := s.command(request.CommandID)
		if command == nil {
			return nil, sandbox.ErrStale
		}
		return empty{}, command.Signal(request.Signal)
	case opCommandKill:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		command := s.command(request.ID)
		if command == nil {
			return nil, sandbox.ErrStale
		}
		return empty{}, command.Kill()
	case opFS:
		return s.handleFS(ctx, body)
	case opFileOpen:
		var request fileOpenRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		instance := s.sandbox(request.SandboxID)
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		files, err := instance.FS(request.Root)
		if err != nil {
			return nil, err
		}
		file, err := files.Open(ctx, request.Name, request.Options)
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(file)
		return idResponse{ID: id}, err
	case opFileStat, opFileSync, opFileClose:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		file := s.file(request.ID)
		if file == nil {
			return nil, sandbox.ErrStale
		}
		switch operation {
		case opFileStat:
			info, err := file.Stat()
			if err != nil {
				return nil, err
			}
			return fsResponse{Info: makeWireFileInfo(info)}, nil
		case opFileSync:
			return empty{}, file.Sync()
		default:
			err := file.Close()
			s.mu.Lock()
			delete(s.files, request.ID)
			s.mu.Unlock()
			return empty{}, err
		}
	case opStreamClose:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		stream, ok := s.streams[request.ID]
		if ok {
			delete(s.streams, request.ID)
		}
		s.mu.Unlock()
		if !ok {
			return nil, sandbox.ErrStale
		}
		return empty{}, stream.closer.Close()
	case opDial, opListen, opListenPacket, opDialUnix, opListenUnix:
		return s.handleNetwork(ctx, operation, body)
	case opConnDeadline, opConnClose, opListenerClose, opPacketDeadline, opPacketClose:
		return s.handleNetworkControl(operation, body)
	default:
		return nil, sandbox.ErrUnsupported
	}
}

func (s *Server) handleData(ctx context.Context, operation string, body json.RawMessage) (any, error) {
	switch operation {
	case opCommandChunk:
		var request commandChunkRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		if len(request.Data) == 0 || len(request.Data) > maxDataChunk {
			return nil, sandbox.ErrInvalid
		}
		s.mu.Lock()
		upload := s.uploads[request.UploadID]
		if upload == nil || uint32(len(upload.data)) != request.Offset || len(upload.data)+len(request.Data) > int(upload.size) {
			s.mu.Unlock()
			return nil, sandbox.ErrStale
		}
		upload.data = append(upload.data, request.Data...)
		s.mu.Unlock()
		return empty{}, nil
	case opSandboxWait:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		instance := s.sandbox(request.ID)
		if instance == nil {
			return nil, sandbox.ErrStale
		}
		select {
		case <-instance.Done():
			err := instance.Err()
			s.mu.Lock()
			delete(s.sandboxes, request.ID)
			s.mu.Unlock()
			return empty{}, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	case opCommandWait:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		command := s.command(request.ID)
		if command == nil {
			return nil, sandbox.ErrStale
		}
		exit, err := command.Wait()
		s.mu.Lock()
		delete(s.commands, request.ID)
		s.mu.Unlock()
		var exitError *sandbox.ExitError
		if err != nil && !errors.As(err, &exitError) {
			return nil, err
		}
		return commandWaitResponse{Exit: exit, ExitError: exitError != nil}, nil
	case opFileRead, opFileWrite, opFileSeek:
		var request fileIORequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		file := s.file(request.ID)
		if file == nil {
			return nil, sandbox.ErrStale
		}
		switch operation {
		case opFileRead:
			return readResource(file, request.Count)
		case opFileWrite:
			return writeResource(file, request.Data)
		default:
			offset, err := file.Seek(request.Offset, request.Whence)
			return seekResponse{Offset: offset}, err
		}
	case opStreamRead, opStreamWrite:
		var request fileIORequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		s.mu.Lock()
		stream, ok := s.streams[request.ID]
		s.mu.Unlock()
		if !ok {
			return nil, sandbox.ErrStale
		}
		if operation == opStreamRead {
			if stream.reader == nil {
				return nil, sandbox.ErrDenied
			}
			return readResource(stream.reader, request.Count)
		}
		if stream.writer == nil {
			return nil, sandbox.ErrDenied
		}
		return writeResource(stream.writer, request.Data)
	case opConnRead, opConnWrite:
		var request fileIORequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		connection := s.connection(request.ID)
		if connection == nil {
			return nil, sandbox.ErrStale
		}
		if operation == opConnRead {
			return readResource(connection, request.Count)
		}
		return writeResource(connection, request.Data)
	case opAccept:
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		listener := s.listener(request.ID)
		if listener == nil {
			return nil, sandbox.ErrStale
		}
		connection, err := listener.Accept()
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(connection)
		return networkResponse{ID: id, Local: makeWireAddr(connection.LocalAddr()), Remote: makeWireAddr(connection.RemoteAddr())}, err
	case opPacketRead:
		var request fileIORequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		packet := s.packet(request.ID)
		if packet == nil || request.Count < 1 || request.Count > maxDataChunk {
			return nil, sandbox.ErrInvalid
		}
		data := make([]byte, request.Count)
		n, address, err := packet.ReadFrom(data)
		return packetReadResponse{Data: data[:n], Addr: makeWireAddr(address)}, err
	case opPacketWrite:
		var request packetWriteRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		packet := s.packet(request.ID)
		address, err := parseWireAddr(request.Addr)
		if packet == nil || err != nil || len(request.Data) > maxDataChunk {
			return nil, sandbox.ErrInvalid
		}
		n, err := packet.WriteTo(request.Data, address)
		return dataResponse{Count: n}, err
	default:
		return nil, sandbox.ErrUnsupported
	}
}

func (s *Server) handleFS(ctx context.Context, body json.RawMessage) (any, error) {
	var request fsRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	instance := s.sandbox(request.SandboxID)
	if instance == nil {
		return nil, sandbox.ErrStale
	}
	files, err := instance.FS(request.Root)
	if err != nil {
		return nil, err
	}
	switch request.Action {
	case "mkdir_all":
		return empty{}, files.MkdirAll(ctx, request.Name, fs.FileMode(request.Mode))
	case "temp_dir":
		name, err := files.TempDir(ctx, request.Name, request.Pattern)
		return fsResponse{Name: name}, err
	case "rename":
		return empty{}, files.Rename(ctx, request.Name, request.Other)
	case "stat":
		info, err := files.Stat(ctx, request.Name)
		if err != nil {
			return nil, err
		}
		return fsResponse{Info: makeWireFileInfo(info)}, nil
	case "read_dir":
		entries, err := files.ReadDir(ctx, request.Name, int(request.Maximum))
		if err != nil {
			return nil, err
		}
		result := make([]wireDirEntry, 0, len(entries))
		for _, entry := range entries {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return nil, infoErr
			}
			result = append(result, wireDirEntry{Name: entry.Name(), Type: uint32(entry.Type()), Info: makeWireFileInfo(info)})
		}
		return fsResponse{Entries: result}, nil
	case "remove":
		return empty{}, files.Remove(ctx, request.Name)
	case "remove_all":
		return empty{}, files.RemoveAll(ctx, request.Name)
	default:
		return nil, sandbox.ErrInvalid
	}
}

func (s *Server) handleNetwork(ctx context.Context, operation string, body json.RawMessage) (any, error) {
	var request networkRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	instance := s.sandbox(request.SandboxID)
	if instance == nil {
		return nil, sandbox.ErrStale
	}
	switch operation {
	case opDial, opDialUnix:
		var connection net.Conn
		var err error
		if operation == opDial {
			connection, err = instance.DialContext(ctx, request.Network, request.Address)
		} else {
			connection, err = instance.DialUnix(ctx, request.Path)
		}
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(connection)
		return networkResponse{ID: id, Local: makeWireAddr(connection.LocalAddr()), Remote: makeWireAddr(connection.RemoteAddr())}, err
	case opListen, opListenUnix:
		var listener net.Listener
		var err error
		if operation == opListen {
			listener, err = instance.Listen(ctx, request.Network, request.Address)
		} else {
			listener, err = instance.ListenUnix(ctx, request.Path)
		}
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(listener)
		return networkResponse{ID: id, Local: makeWireAddr(listener.Addr())}, err
	case opListenPacket:
		packet, err := instance.ListenPacket(ctx, request.Network, request.Address)
		if err != nil {
			return nil, err
		}
		id, err := s.allocate(packet)
		return networkResponse{ID: id, Local: makeWireAddr(packet.LocalAddr())}, err
	default:
		return nil, sandbox.ErrInvalid
	}
}

func (s *Server) handleNetworkControl(operation string, body json.RawMessage) (any, error) {
	if operation == opConnClose || operation == opListenerClose || operation == opPacketClose {
		var request idRequest
		if err := decodeBody(body, &request); err != nil {
			return nil, err
		}
		switch operation {
		case opConnClose:
			s.mu.Lock()
			value := s.conns[request.ID]
			delete(s.conns, request.ID)
			s.mu.Unlock()
			if value != nil {
				return empty{}, value.Close()
			}
		case opListenerClose:
			s.mu.Lock()
			value := s.listeners[request.ID]
			delete(s.listeners, request.ID)
			s.mu.Unlock()
			if value != nil {
				return empty{}, value.Close()
			}
		case opPacketClose:
			s.mu.Lock()
			value := s.packets[request.ID]
			delete(s.packets, request.ID)
			s.mu.Unlock()
			if value != nil {
				return empty{}, value.Close()
			}
		}
		return nil, sandbox.ErrStale
	}
	var request deadlineRequest
	if err := decodeBody(body, &request); err != nil {
		return nil, err
	}
	deadline := wireTime(request.UnixNano)
	if operation == opPacketDeadline {
		packet := s.packet(request.ID)
		if packet == nil {
			return nil, sandbox.ErrStale
		}
		return empty{}, setPacketDeadline(packet, request.Kind, deadline)
	}
	connection := s.connection(request.ID)
	if connection == nil {
		return nil, sandbox.ErrStale
	}
	return empty{}, setConnDeadline(connection, request.Kind, deadline)
}

func readResource(reader io.Reader, count int) (dataResponse, error) {
	if count < 1 || count > maxDataChunk {
		return dataResponse{}, sandbox.ErrInvalid
	}
	data := make([]byte, count)
	n, err := reader.Read(data)
	if n > 0 {
		err = nil
	}
	return dataResponse{Data: data[:n], Count: n}, err
}

func writeResource(writer io.Writer, data []byte) (dataResponse, error) {
	if len(data) == 0 || len(data) > maxDataChunk {
		return dataResponse{}, sandbox.ErrInvalid
	}
	n, err := writer.Write(data)
	return dataResponse{Count: n}, err
}

func (s *Server) sandbox(id uint64) sandbox.Sandbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sandboxes[id]
}
func (s *Server) command(id uint64) sandbox.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commands[id]
}
func (s *Server) file(id uint64) sandbox.File   { s.mu.Lock(); defer s.mu.Unlock(); return s.files[id] }
func (s *Server) connection(id uint64) net.Conn { s.mu.Lock(); defer s.mu.Unlock(); return s.conns[id] }
func (s *Server) listener(id uint64) net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listeners[id]
}
func (s *Server) packet(id uint64) net.PacketConn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.packets[id]
}

func makeWireAddr(address net.Addr) wireAddr {
	if address == nil {
		return wireAddr{}
	}
	return wireAddr{Network: address.Network(), Value: address.String()}
}

func parseWireAddr(address wireAddr) (net.Addr, error) {
	switch address.Network {
	case "udp", "udp4", "udp6":
		return net.ResolveUDPAddr(address.Network, address.Value)
	case "tcp", "tcp4", "tcp6":
		return net.ResolveTCPAddr(address.Network, address.Value)
	case "unix", "unixgram", "unixpacket":
		return net.ResolveUnixAddr(address.Network, address.Value)
	default:
		return nil, sandbox.ErrInvalid
	}
}

func setConnDeadline(connection net.Conn, kind string, deadline time.Time) error {
	switch kind {
	case "all":
		return connection.SetDeadline(deadline)
	case "read":
		return connection.SetReadDeadline(deadline)
	case "write":
		return connection.SetWriteDeadline(deadline)
	default:
		return sandbox.ErrInvalid
	}
}

func setPacketDeadline(connection net.PacketConn, kind string, deadline time.Time) error {
	switch kind {
	case "all":
		return connection.SetDeadline(deadline)
	case "read":
		return connection.SetReadDeadline(deadline)
	case "write":
		return connection.SetWriteDeadline(deadline)
	default:
		return sandbox.ErrInvalid
	}
}
