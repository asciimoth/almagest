package isolated

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/subprogram/policy"
	"github.com/asciimoth/almagest/bin/internal/subprogram/service"
	"github.com/asciimoth/almagest/lib/sandbox"
)

type rejectingService struct{}

func (rejectingService) BindOwner(context.Context, sandbox.Owner) (sandbox.OwnerSandboxes, error) {
	return nil, sandbox.ErrDenied
}
func (rejectingService) RebindOwner(context.Context, sandbox.OwnerRename) (sandbox.Owner, error) {
	return sandbox.Owner{}, sandbox.ErrDenied
}
func (rejectingService) ReleaseOwner(context.Context, sandbox.Owner) error { return sandbox.ErrDenied }
func (rejectingService) Close() error                                      { return nil }

func fuzzServer() *Server {
	return &Server{
		service: rejectingService{}, owners: make(map[uint64]sandbox.OwnerSandboxes),
		sandboxes: make(map[uint64]sandbox.Sandbox), commands: make(map[uint64]sandbox.Command),
		uploads: make(map[uint64]*commandUpload), streams: make(map[uint64]streamResource),
		files: make(map[uint64]sandbox.File), conns: make(map[uint64]net.Conn),
		listeners: make(map[uint64]net.Listener), packets: make(map[uint64]net.PacketConn),
	}
}

func FuzzSupervisorRequestBodies(f *testing.F) {
	control := []string{
		opServiceStatus, opIssueBind, opBind, opRebind, opRelease, opRevoke,
		opAcquire, opRemove, opSandboxStatus, opSandboxClose, opChildPath,
		opProgramPath, opProcess, opNewCommand, opCommandUpload, opCommandCommit,
		opCommandAbort, opCommandPipe, opCommandStart, opCommandSignal, opCommandKill,
		opFS, opFileOpen, opFileStat, opFileSync, opFileClose, opStreamClose,
		opDial, opListen, opListenPacket, opDialUnix, opListenUnix, opConnDeadline,
		opConnClose, opListenerClose, opPacketDeadline, opPacketClose,
	}
	data := []string{
		opCommandChunk, opSandboxWait, opCommandWait, opFileRead, opFileWrite,
		opFileSeek, opStreamRead, opStreamWrite, opConnRead, opConnWrite, opAccept,
		opPacketRead, opPacketWrite,
	}
	for _, operation := range control {
		f.Add(false, operation, []byte(`{}`))
	}
	for _, operation := range data {
		f.Add(true, operation, []byte(`{}`))
	}
	for _, body := range [][]byte{
		{}, []byte(`null`), []byte(`{"id":1,"id":2}`),
		[]byte(`{"id":18446744073709551615,"unknown":true}`),
		[]byte(`{"sandbox_id":1,"path":{"Root":0,"Rel":"../host"}}`),
	} {
		f.Add(false, opChildPath, body)
	}
	f.Fuzz(func(t *testing.T, dataLane bool, operation string, body []byte) {
		server := fuzzServer()
		var value any
		var err error
		if dataLane {
			value, err = server.handleData(context.Background(), operation, json.RawMessage(body))
		} else {
			value, err = server.handleControl(context.Background(), operation, json.RawMessage(body))
		}
		if err == nil {
			if _, marshalErr := json.Marshal(value); marshalErr != nil {
				t.Fatalf("accepted response cannot be encoded: %v", marshalErr)
			}
		}
	})
}

func TestIsolatedHelperProcess(t *testing.T) {
	if os.Getenv("ALMAGEST_ISOLATED_HELPER") != "1" {
		return
	}
	fmt.Print("isolated-ok")
	os.Exit(0)
}

func TestRemoteServiceContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ALMAGEST_ISOLATED_HELPER", "1")
	config := policy.Config{Mode: sandbox.Off, TemporaryRoot: filepath.Join(root, "temporary"), RegistryRoot: filepath.Join(root, "registry"),
		Limits: policy.ServiceLimits{Instances: 4, Commands: 4, RelayBytes: 1 << 20, Receipts: 32},
		Programs: map[sandbox.ProgramID]policy.Program{"helper": {Executable: os.Args[0], Builder: "fixed-v1",
			Arguments: []policy.Argument{{Literal: "-test.run=TestIsolatedHelperProcess"}, {Input: "payload"}},
			Inputs:    map[string]policy.Input{"payload": {Kind: sandbox.TextValue, Required: true, Maximum: 100 << 10}}}},
		Templates: map[sandbox.TemplateID]policy.Template{"test": {AllowedClasses: []string{"test/Node"}, Programs: []sandbox.ProgramID{"helper"},
			RuntimeProfile: "direct-v1", PersistentRoot: filepath.Join(root, "persistent"), Environment: policy.Environment{Set: map[string]string{"ALMAGEST_ISOLATED_HELPER": "1"}},
			Limits: policy.TemplateLimits{Commands: 2, Files: 16, Sockets: 16, TransferBytes: 1 << 20, TemporaryBytes: 1 << 20, OutputBytes: 1 << 20}}}}
	backend, err := service.NewDirect(config, policy.Probe{Platform: runtime.GOOS, EffectiveIdentity: "test",
		Available: map[sandbox.Feature]bool{sandbox.Environment: true, sandbox.HandleInheritance: true}})
	if err != nil {
		t.Fatal(err)
	}
	controlServer, controlClient := net.Pipe()
	dataServer, dataClient := net.Pipe()
	sessionID, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{ID: sessionID, Epoch: 1, Version: protocol.Version}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverResult := make(chan struct {
		server *Server
		err    error
	}, 1)
	go func() {
		server, serverErr := NewServer(ctx, controlServer, dataServer, session, backend)
		serverResult <- struct {
			server *Server
			err    error
		}{server, serverErr}
	}()
	client, err := NewClient(ctx, controlClient, dataClient, session)
	if err != nil {
		t.Fatal(err)
	}
	result := <-serverResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.server.Close() //nolint:errcheck
	defer client.Close()        //nolint:errcheck

	owner, bound, err := client.IssueAndBindOwner(ctx, sandbox.Owner{Workspace: "main", Epoch: 1, Class: "test/Node", Name: "node", NameRevision: 1})
	if err != nil || owner.Key == "" {
		t.Fatalf("bind owner: owner=%+v err=%v", owner, err)
	}
	instance, err := bound.Acquire(ctx, "main", "test")
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := instance.FS(sandbox.Temporary)
	if err != nil {
		t.Fatal(err)
	}
	if err := temporary.WriteFile(ctx, "value", []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := temporary.ReadFile(ctx, "value", 32)
	if err != nil || string(data) != "payload" {
		t.Fatalf("read file: %q %v", data, err)
	}
	command, err := instance.NewCommand("helper", sandbox.Launch{Directory: sandbox.Path{Root: sandbox.Temporary},
		Inputs: map[string]sandbox.Value{"payload": {Kind: sandbox.TextValue, Text: "small"}}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := sandbox.Output(ctx, command, 128)
	if err != nil || !strings.Contains(string(output), "isolated-ok") {
		t.Fatalf("command output: %q %v", output, err)
	}
	// This value is larger than one control frame. NewCommand must transfer it
	// through bounded data chunks and preserve the public service contract.
	if _, err := instance.NewCommand("helper", sandbox.Launch{Inputs: map[string]sandbox.Value{
		"payload": {Kind: sandbox.TextValue, Text: strings.Repeat("x", 64<<10)},
	}}); err != nil {
		t.Fatalf("large command launch: %v", err)
	}
	listener, err := instance.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	connection, err := instance.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	peer := <-accepted
	if peer == nil {
		t.Fatal("accept failed")
	}
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	if _, err := peer.Read(buffer); err != nil || string(buffer) != "x" {
		t.Fatalf("connection relay: %q %v", buffer, err)
	}
	_ = connection.Close()
	_ = peer.Close()
	_ = listener.Close()
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.ReleaseOwner(ctx, owner); err != nil {
		t.Fatal(err)
	}
}
