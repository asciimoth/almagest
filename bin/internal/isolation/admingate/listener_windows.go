//go:build windows

package admingate

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"unicode"

	"github.com/asciimoth/almagest/bin/internal/windowsadmin"
	"github.com/asciimoth/gonnect/sockowner"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.zx2c4.com/wireguard/ipc/namedpipe"
)

const localPipePrefix = `\\.\pipe\`

var (
	getIncomingOwner = sockowner.GetIncomingConnOwner
	lookupAccountSID = windows.LookupSID
	getProcessSID    = processUserSID
	impersonatePipe  = impersonateNamedPipeClient
	revertIdentity   = windows.RevertToSelf
	queryThreadUser  = queryEffectiveThreadUser
)

func listenWeb(address string, filters Filters) (net.Listener, error) {
	if len(filters.UIDs) != 0 || len(filters.GIDs) != 0 {
		return nil, errors.New("windows web admin does not support UID or GID filters; use username filters")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	if !filters.enabled() {
		return listener, nil
	}
	checker := &windowsTCPChecker{}
	for _, name := range filters.Usernames {
		sid, _, _, err := lookupAccountSID("", name)
		if err != nil || sid == nil || sid.String() == "" {
			_ = listener.Close()
			return nil, fmt.Errorf("windows web admin filter resolve username %q: %w", name, err)
		}
		checker.usernames = append(checker.usernames, windowsUsernameRule{name: name, sid: sid.String()})
	}
	return &windowsTCPListener{Listener: listener, checker: checker}, nil
}

type windowsTCPListener struct {
	net.Listener
	checker *windowsTCPChecker
}

type windowsTCPChecker struct{ usernames []windowsUsernameRule }
type windowsUsernameRule struct{ name, sid string }

func (l *windowsTCPListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.checker.allow(conn) {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func (c *windowsTCPChecker) allow(conn net.Conn) bool {
	owner, err := getIncomingOwner(conn)
	if err != nil || owner == nil || len(owner.PIDs) != 1 || owner.PIDs[0] <= 0 {
		return false
	}
	sid, err := getProcessSID(uint32(owner.PIDs[0])) //nolint:gosec // Windows PIDs use uint32.
	if err != nil {
		return false
	}
	for _, rule := range c.usernames {
		current, _, _, err := lookupAccountSID("", rule.name)
		if err == nil && current != nil && current.String() == rule.sid && sid == rule.sid {
			return true
		}
	}
	return false
}

func processUserSID(pid uint32) (string, error) {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	var token windows.Token
	if err = windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	user, err := token.GetTokenUser()
	if err != nil || user == nil || user.User.Sid == nil {
		return "", fmt.Errorf("process token has no user SID: %w", err)
	}
	return user.User.Sid.String(), nil
}

func listenPipe(path string, filters Filters) (net.Listener, error) {
	if filters.enabled() {
		return nil, errors.New("windows pipe access is fixed by its protected DACL")
	}
	if err := validateLocalPipePath(path); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("get Windows pipe owner: %w", err)
	}
	service, err := svc.IsWindowsService()
	if err != nil {
		return nil, fmt.Errorf("detect Windows service for admin pipe: %w", err)
	}
	owner := user.User.Sid
	dacl := fmt.Sprintf("(A;;GA;;;SY)(A;;GA;;;%s)", owner.String())
	if service {
		if !owner.IsWellKnown(windows.WinLocalSystemSid) {
			return nil, errors.New("machine admin pipe requires the supported Local System service identity")
		}
		dacl, err = windowsadmin.MachinePipeDACL()
		if err != nil {
			return nil, err
		}
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("O:%sD:P%s", owner.String(), dacl))
	if err != nil {
		return nil, fmt.Errorf("build Windows admin pipe ACL: %w", err)
	}
	listener, err := (&namedpipe.ListenConfig{SecurityDescriptor: descriptor, InputBufferSize: 64 * 1024, OutputBufferSize: 64 * 1024}).Listen(path)
	if err != nil {
		return nil, fmt.Errorf("listen on Windows admin pipe %s: %w", path, err)
	}
	return &windowsPipeListener{Listener: listener}, nil
}

type windowsPipeListener struct{ net.Listener }

func (l *windowsPipeListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if err = inspectPipeClient(conn); err == nil {
			return conn, nil
		}
		_ = conn.Close()
	}
}

// inspectPipeClient uses a dedicated goroutine so a failed RevertToSelf cannot
// return an impersonating thread to the runtime pool. The OS DACL remains the
// access decision. No work except the token query runs under the client token.
func inspectPipeClient(conn net.Conn) error {
	holder, ok := conn.(interface{ Handle() windows.Handle })
	if !ok {
		return errors.New("windows admin pipe connection does not expose its handle")
	}
	done := make(chan error, 1)
	go func() { done <- inspectPipeClientOnThread(holder.Handle()) }()
	return <-done
}

func inspectPipeClientOnThread(handle windows.Handle) error {
	runtime.LockOSThread()
	if err := impersonatePipe(handle); err != nil {
		runtime.UnlockOSThread()
		return fmt.Errorf("impersonate Windows admin pipe client: %w", err)
	}
	queryErr := queryThreadUser()
	revertErr := revertIdentity()
	if revertErr != nil {
		// Exiting this goroutine while it remains locked makes the Go runtime
		// terminate the OS thread instead of scheduling unrelated work on it.
		return errors.Join(queryErr, fmt.Errorf("revert Windows admin pipe client identity: %w", revertErr))
	}
	runtime.UnlockOSThread()
	// Existing clients request SecurityAnonymous. The protected pipe DACL is
	// the authorization check, so preserve that access behavior when Windows
	// intentionally withholds an anonymous token's user. Other query failures
	// fail closed.
	if queryErr != nil && !errors.Is(queryErr, windows.ERROR_CANT_OPEN_ANONYMOUS) &&
		!errors.Is(queryErr, windows.ERROR_BAD_IMPERSONATION_LEVEL) {
		return fmt.Errorf("query Windows admin pipe client identity: %w", queryErr)
	}
	return nil
}

func queryEffectiveThreadUser() error {
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil {
		return err
	}
	if user == nil || user.User.Sid == nil {
		return errors.New("token has no user SID")
	}
	return nil
}

func impersonateNamedPipeClient(handle windows.Handle) error {
	proc := windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient")
	ok, _, callErr := proc.Call(uintptr(handle))
	if ok == 0 {
		return callErr
	}
	return nil
}

func validateLocalPipePath(path string) error {
	if len(path) <= len(localPipePrefix) || !strings.EqualFold(path[:len(localPipePrefix)], localPipePrefix) {
		return errors.New(`windows admin pipe must use the local \\.\pipe\ namespace`)
	}
	name := path[len(localPipePrefix):]
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return errors.New("windows admin pipe name must be one component")
	}
	if len(name) > 200 {
		return errors.New("windows admin pipe name is too long")
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("windows admin pipe name contains a control character")
	}
	return nil
}
