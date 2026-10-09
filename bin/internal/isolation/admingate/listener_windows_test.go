//go:build windows

package admingate

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/sockowner"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

func TestWindowsPublicPipeRelaysAndRejectsCollision(t *testing.T) {
	path := fmt.Sprintf(`\\.\pipe\almagest-admingate-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	listener, err := Listen(EndpointConfig{ID: 1, Kind: EndpointPipe, Address: path})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate, err := Listen(EndpointConfig{ID: 2, Kind: EndpointPipe, Address: path}); err == nil {
		_ = duplicate.Close()
		t.Fatal("second public pipe listener claimed an occupied name")
	}
	_, runtime := newGatePair(t, []Endpoint{{ID: 1, Listener: listener}}, []uint16{1})
	relay, _ := runtime.Listener(1)
	owner, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	client, err := (&namedpipe.DialConfig{ExpectedOwner: owner.User.Sid}).DialContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	server, err := relay.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	go func() { _, _ = client.Write([]byte("pipe")) }()
	got := make([]byte, 4)
	if _, err = io.ReadFull(server, got); err != nil || string(got) != "pipe" {
		t.Fatalf("pipe relay read = %q, %v", got, err)
	}
}

func TestWindowsPipeImpersonationAlwaysReverts(t *testing.T) {
	originalImpersonate, originalRevert, originalQuery := impersonatePipe, revertIdentity, queryThreadUser
	t.Cleanup(func() {
		impersonatePipe, revertIdentity, queryThreadUser = originalImpersonate, originalRevert, originalQuery
	})
	impersonated, reverted := false, false
	impersonatePipe = func(windows.Handle) error { impersonated = true; return nil }
	revertIdentity = func() error { reverted = true; return nil }
	queryThreadUser = func() error { return nil }
	if err := inspectPipeClient(fakePipeConn{handle: 42}); err != nil {
		t.Fatal(err)
	}
	if !impersonated || !reverted {
		t.Fatalf("impersonated=%v reverted=%v", impersonated, reverted)
	}

	revertIdentity = func() error { return errors.New("revert sentinel") }
	if err := inspectPipeClient(fakePipeConn{handle: 42}); err == nil || !strings.Contains(err.Error(), "revert sentinel") {
		t.Fatalf("revert failure = %v", err)
	}
	impersonatePipe = func(windows.Handle) error { return errors.New("impersonate sentinel") }
	reverted = false
	if err := inspectPipeClient(fakePipeConn{handle: 42}); err == nil || reverted {
		t.Fatalf("impersonation failure = %v, reverted=%v", err, reverted)
	}
	impersonatePipe = func(windows.Handle) error { return nil }
	queryThreadUser = func() error { return windows.ERROR_CANT_OPEN_ANONYMOUS }
	revertIdentity = func() error { return nil }
	if err := inspectPipeClient(fakePipeConn{handle: 42}); err != nil {
		t.Fatalf("anonymous DACL-approved client was rejected: %v", err)
	}
	queryThreadUser = func() error { return errors.New("query sentinel") }
	if err := inspectPipeClient(fakePipeConn{handle: 42}); err == nil || !strings.Contains(err.Error(), "query sentinel") {
		t.Fatalf("unexpected query failure = %v", err)
	}
}

func TestWindowsTCPPeerGateFailsClosed(t *testing.T) {
	originalOwner, originalLookup, originalProcess := getIncomingOwner, lookupAccountSID, getProcessSID
	t.Cleanup(func() {
		getIncomingOwner, lookupAccountSID, getProcessSID = originalOwner, originalLookup, originalProcess
	})
	allowed, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	if err != nil {
		t.Fatal(err)
	}
	other, err := windows.StringToSid("S-1-5-21-1-2-3-1002")
	if err != nil {
		t.Fatal(err)
	}
	lookupAccountSID = func(_, name string) (*windows.SID, string, uint32, error) {
		if name != "alice" {
			return nil, "", 0, errors.New("unknown")
		}
		return allowed, "TEST", windows.SidTypeUser, nil
	}
	getIncomingOwner = func(net.Conn) (*sockowner.SocketOwner, error) {
		return &sockowner.SocketOwner{PIDs: []int{42}}, nil
	}
	checker := windowsTCPChecker{usernames: []windowsUsernameRule{{name: "alice", sid: allowed.String()}}}
	getProcessSID = func(uint32) (string, error) { return allowed.String(), nil }
	if !checker.allow(nil) {
		t.Fatal("matching exact owner was denied")
	}
	getProcessSID = func(uint32) (string, error) { return other.String(), nil }
	if checker.allow(nil) {
		t.Fatal("wrong owner was allowed")
	}
	getIncomingOwner = func(net.Conn) (*sockowner.SocketOwner, error) {
		return &sockowner.SocketOwner{PIDs: []int{42, 43}}, nil
	}
	if checker.allow(nil) {
		t.Fatal("ambiguous owner was allowed")
	}
}

func TestWindowsListenerValidation(t *testing.T) {
	for _, path := range []string{`\\server\pipe\admin`, `\\.\pipe\parent\child`, `\\.\pipe\..`} {
		if err := validateLocalPipePath(path); err == nil {
			t.Fatalf("validateLocalPipePath(%q) succeeded", path)
		}
	}
	if _, err := Listen(EndpointConfig{ID: 1, Kind: EndpointPipe, Address: `\\.\pipe\admin`, Filters: Filters{Usernames: []string{"alice"}}}); err == nil {
		t.Fatal("runtime-selectable pipe filter was accepted")
	}
	if _, err := Listen(EndpointConfig{ID: 1, Kind: EndpointWeb, Address: "127.0.0.1:0", Filters: Filters{UIDs: []uint32{1}}}); err == nil {
		t.Fatal("Windows UID filter was accepted")
	}
}

type fakePipeConn struct{ handle windows.Handle }

func (c fakePipeConn) Handle() windows.Handle         { return c.handle }
func (fakePipeConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (fakePipeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (fakePipeConn) Close() error                     { return nil }
func (fakePipeConn) LocalAddr() net.Addr              { return testAddress("local") }
func (fakePipeConn) RemoteAddr() net.Addr             { return testAddress("remote") }
func (fakePipeConn) SetDeadline(time.Time) error      { return nil }
func (fakePipeConn) SetReadDeadline(time.Time) error  { return nil }
func (fakePipeConn) SetWriteDeadline(time.Time) error { return nil }
