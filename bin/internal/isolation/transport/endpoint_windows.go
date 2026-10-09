//go:build windows

package transport

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"golang.org/x/sys/windows"
)

// PipePair creates connected, overlapped byte-mode endpoints in bootstrap.
// Both handles are non-inheritable. Launch must use an exact handle list for
// each verified suspended child, close unused copies, apply its restrictions,
// and only then resume. There is no connect-by-name API for the runtime.
// Pipe process IDs identify bootstrap, not the final child; bootstrap's retained
// process handle, verified setup, and exclusive handle delivery bind the role.
func PipePair() (*os.File, *os.File, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	// Restrict the named endpoint to the bootstrap identity. Client access
	// excludes FILE_CREATE_PIPE_INSTANCE; server creation uses the owner token.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return nil, nil, err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, nil, err
	}
	pipeName := `\\.\pipe\almagest-private-` + hex.EncodeToString(nonce[:])
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		return nil, nil, err
	}
	server, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, &sa)
	if err != nil {
		return nil, nil, err
	}
	serverOwned := true
	defer func() {
		if serverOwned {
			_ = windows.CloseHandle(server)
		}
	}()
	// Connect the client before ConnectNamedPipe: this is the documented
	// ERROR_PIPE_CONNECTED path and avoids any unbounded bootstrap accept wait.
	client, err := windows.CreateFile(name, windows.FILE_READ_DATA|windows.FILE_WRITE_DATA|windows.SYNCHRONIZE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, nil, err
	}
	clientOwned := true
	defer func() {
		if clientOwned {
			_ = windows.CloseHandle(client)
		}
	}()
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = windows.CloseHandle(event) }()
	overlap := windows.Overlapped{HEvent: event}
	if err := windows.ConnectNamedPipe(server, &overlap); !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			_ = windows.CancelIoEx(server, &overlap)
			var done uint32
			_ = windows.GetOverlappedResult(server, &overlap, &done, true)
		}
		return nil, nil, protocol.ErrInvalidContract
	}
	var clientPID, serverPID uint32
	if err := windows.GetNamedPipeClientProcessId(server, &clientPID); err != nil {
		return nil, nil, err
	}
	if err := windows.GetNamedPipeServerProcessId(client, &serverPID); err != nil {
		return nil, nil, err
	}
	if clientPID != windows.GetCurrentProcessId() || serverPID != windows.GetCurrentProcessId() {
		return nil, nil, protocol.ErrInvalidContract
	}
	serverOwned, clientOwned = false, false
	return os.NewFile(uintptr(server), pipeName), os.NewFile(uintptr(client), pipeName), nil
}

// BindPipe consumes an endpoint from the exact inherited handle list. Numeric
// handles received in a control frame are never accepted. os.File uses Go's
// completion-port poller for these overlapped pipe handles, including deadlines.
func BindPipe(f *os.File) (Stream, error) {
	typ, err := windows.GetFileType(windows.Handle(f.Fd()))
	if err != nil || typ != windows.FILE_TYPE_PIPE {
		_ = f.Close()
		return nil, protocol.ErrInvalidContract
	}
	if err := f.SetReadDeadline(time.Time{}); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
