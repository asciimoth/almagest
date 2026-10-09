//go:build windows

package windowsprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/sockowner"
	"golang.org/x/sys/windows"
)

type probeInput struct {
	Secret, Foreign, Address, DLL string
	Parent                        uint32
	Hang                          bool
}

func init() {
	if os.Getenv("ALMAGEST_ROLE") == "" {
		return
	}
	value, _ := strconv.ParseUint(os.Getenv("ALMAGEST_PIPE_0"), 10, 64)
	pipe := os.NewFile(uintptr(value), "profile-probe")
	var input probeInput
	if json.NewDecoder(pipe).Decode(&input) != nil {
		os.Exit(91)
	}
	if input.Hang {
		for {
			time.Sleep(time.Hour)
		}
	}
	results := make(chan string, 8)
	var wg sync.WaitGroup
	var started sync.WaitGroup
	ready := make(chan struct{})
	for n := 0; n < 8; n++ {
		wg.Add(1)
		started.Add(1)
		go func() {
			defer wg.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			started.Done()
			<-ready
			results <- probeThread(input)
		}()
	}
	started.Wait()
	close(ready)
	wg.Wait()
	close(results)
	var failures []string
	for failure := range results {
		if failure != "" {
			failures = append(failures, failure)
		}
	}
	if os.Getenv("PATH") != "" || os.Getenv("PROFILE_PARENT_SECRET") != "" {
		failures = append(failures, "inherited environment")
	}
	if err := json.NewEncoder(pipe).Encode(failures); err != nil {
		os.Exit(92)
	}
	_ = pipe.Close()
	os.Exit(0)
}

func probeThread(in probeInput) string {
	role := Role(os.Getenv("ALMAGEST_ROLE"))
	var sid *windows.SID
	if role != Network {
		data, err := tokenInfo(windows.GetCurrentProcessToken(), 31)
		if err != nil {
			return err.Error()
		}
		sid = *(**windows.SID)(unsafe.Pointer(&data[0]))
	}
	if err := verifyProcess(windows.CurrentProcess(), sid); err != nil {
		return err.Error()
	}
	if role != Network {
		for _, path := range []string{in.Secret, in.Foreign} {
			_, err := os.ReadFile(path)
			if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				return fmt.Sprintf("protected read %s: %v", path, err)
			}
		}
		process, err := windows.OpenProcess(windows.PROCESS_DUP_HANDLE|windows.PROCESS_QUERY_INFORMATION, false, in.Parent)
		if err == nil {
			_ = windows.CloseHandle(process)
			return "opened broker process"
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return "broker denial was not access denied"
		}
		if dll, err := windows.LoadDLL(in.DLL); err == nil {
			_ = dll.Release()
			return "loaded Low DLL"
		} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_INVALID_IMAGE_HASH) {
			return "Low DLL failed for unrelated reason: " + err.Error()
		}
		manager := windows.NewLazySystemDLL("advapi32.dll").NewProc("OpenSCManagerW")
		h, _, err := manager.Call(0, 0, 2) // SC_MANAGER_CREATE_SERVICE
		if h != 0 {
			_ = windows.CloseServiceHandle(windows.Handle(h))
			return "can install a driver service"
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return "driver service denial: " + err.Error()
		}
	}
	connection, err := net.DialTimeout("tcp4", in.Address, 2*time.Second)
	if connection != nil {
		defer func() { _ = connection.Close() }()
	}
	if role == Network && err != nil {
		return "N socket: " + err.Error()
	}
	if role == Network {
		local := connection.LocalAddr().(*net.TCPAddr)
		remote := connection.RemoteAddr().(*net.TCPAddr)
		owner, err := sockowner.GetSockOwner(sockowner.FlowTuple{Proto: "tcp", LocalIP: local.IP, LocalPort: uint16(local.Port), RemoteIP: remote.IP, RemotePort: uint16(remote.Port)})
		if err != nil || owner == nil || len(owner.PIDs) != 1 || owner.PIDs[0] != os.Getpid() {
			return fmt.Sprintf("N native owner lookup: %+v %v", owner, err)
		}
		addresses, err := net.LookupHost("localhost")
		if err != nil || len(addresses) == 0 {
			return fmt.Sprintf("N resolver: %v", err)
		}
		packet, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			return "N UDP listener: " + err.Error()
		}
		defer func() { _ = packet.Close() }()
		if err := packet.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			return err.Error()
		}
		if _, err := packet.WriteTo([]byte("probe"), packet.LocalAddr()); err != nil {
			return err.Error()
		}
		var data [8]byte
		if n, _, err := packet.ReadFrom(data[:]); err != nil || string(data[:n]) != "probe" {
			return fmt.Sprintf("N UDP exchange: %v", err)
		}
	}
	// This Windows build can report its AppContainer loopback drop as a native
	// WSAETIMEDOUT. The parent proves the same listener is reachable before and
	// after the probe and checks that no restricted connection was accepted.
	if role != Network && !errors.Is(err, windows.WSAEACCES) && !errors.Is(err, windows.WSAETIMEDOUT) {
		return fmt.Sprintf("native socket denial: %v", err)
	}
	file, err := os.CreateTemp(os.Getenv("ALMAGEST_STORAGE"), "thread-")
	if err != nil {
		return "private storage: " + err.Error()
	}
	name := file.Name()
	_ = file.Close()
	_ = os.Remove(name)
	// A real, readable executable avoids accepting a path/loader error as proof
	// of job containment. Both ordinary and explicit breakaway creation fail.
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err.Error()
	}
	app, _ := windows.UTF16PtrFromString(filepath.Join(system, "cmd.exe"))
	for _, extra := range []uint32{0, windows.CREATE_BREAKAWAY_FROM_JOB} {
		cmd, _ := windows.UTF16PtrFromString("cmd.exe /d /c exit 0")
		si := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
		var pi windows.ProcessInformation
		err := windows.CreateProcess(app, cmd, nil, nil, false, windows.CREATE_NO_WINDOW|extra, nil, nil, &si, &pi)
		if err == nil {
			_ = windows.TerminateProcess(pi.Process, 1)
			closeHandles([]windows.Handle{pi.Process, pi.Thread})
			return "created child"
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && (extra != 0 || !errors.Is(err, windows.ERROR_NOT_ENOUGH_QUOTA)) {
			return "child creation failed for unrelated reason: " + err.Error()
		}
	}
	return ""
}

func fixture(t *testing.T) (string, string) {
	t.Helper()
	parent := "."
	if windows.GetCurrentProcessToken().IsElevated() {
		// Service-derived tests must not execute code from the ordinary user's
		// source tree. Provision a separate protected fixture for that authority.
		parent = os.TempDir()
	}
	root, err := os.MkdirTemp(parent, ".windows-profile-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;S-1-5-32-544)(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "probe.exe")
	copyFile(t, exe, target)
	return root, target
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(to, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func identity(t *testing.T, role Role, root, exe string) (*Identity, string) {
	t.Helper()
	i, err := NewIdentity(role, fmt.Sprintf("test-%d-%d", os.Getpid(), time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := i.Close(); err != nil {
			t.Error(err)
		}
	})
	storage := filepath.Join(root, string(role))
	if err = i.ProvisionDirectory(storage); err != nil {
		t.Fatal(err)
	}
	if err = i.GrantExecutable(exe); err != nil {
		t.Fatal(err)
	}
	return i, storage
}

func TestNativeRoles(t *testing.T) {
	if err := CheckPlatform(windows.RtlGetVersion().BuildNumber, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROFILE_PARENT_SECRET", "must-not-inherit")
	for _, role := range []Role{Runtime, Writer, Command, Network} {
		t.Run(string(role), func(t *testing.T) {
			root, exe := fixture(t)
			other, otherStore := identity(t, Writer, root, exe)
			_ = other
			foreign := filepath.Join(otherStore, "foreign.txt")
			if err := os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(root, "protected.txt")
			if err := os.WriteFile(secret, []byte("protected"), 0600); err != nil {
				t.Fatal(err)
			}
			var i *Identity
			storage := filepath.Join(root, "network")
			if role != Network {
				// A separate root avoids using C's directory for a second identity.
				own := filepath.Join(root, "own")
				if err := os.Mkdir(own, 0700); err != nil {
					t.Fatal(err)
				}
				i, storage = identity(t, role, own, exe)
			} else if err := os.Mkdir(storage, 0700); err != nil {
				t.Fatal(err)
			}
			system, err := windows.GetSystemDirectory()
			if err != nil {
				t.Fatal(err)
			}
			dll := filepath.Join(storage, "untrusted.dll")
			copyFile(t, filepath.Join(system, "version.dll"), dll)
			loaded, err := windows.LoadDLL(dll)
			if err != nil {
				t.Fatalf("DLL positive control: %v", err)
			}
			if err := loaded.Release(); err != nil {
				t.Fatal(err)
			}
			server, client, err := transport.PipePair()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = server.Close(); _ = client.Close() }()
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			var accepted atomic.Int32
			go func() {
				for {
					c, e := listener.Accept()
					if e != nil {
						return
					}
					accepted.Add(1)
					_ = c.Close()
				}
			}()
			controlConnection(t, listener.Addr().String())
			request := Request{Plan: DefaultPlan(role), Identity: i, Executable: exe, Storage: storage, Pipes: []windows.Handle{windows.Handle(client.Fd())}}
			p, err := Launch(context.Background(), request)
			if role == Network && windows.GetCurrentProcessToken().IsElevated() {
				if !errors.Is(err, ErrUnavailable) || p != nil {
					t.Fatalf("accepted service N: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = p.Close() }()
			checkJob(t, p, request.Plan)
			_ = client.Close()
			if err := server.SetDeadline(time.Now().Add(20 * time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := json.NewEncoder(server).Encode(probeInput{Secret: secret, Foreign: foreign, Address: listener.Addr().String(), DLL: dll, Parent: uint32(os.Getpid())}); err != nil {
				t.Fatal(err)
			}
			var failures []string
			if err := json.NewDecoder(server).Decode(&failures); err != nil {
				t.Fatal(err)
			}
			if len(failures) != 0 {
				t.Fatal(failures)
			}
			controlConnection(t, listener.Addr().String())
			want := int32(2)
			if role == Network {
				want += 8
			}
			if accepted.Load() != want {
				t.Fatalf("accepted %d connections, want %d", accepted.Load(), want)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if code, err := p.Wait(ctx); err != nil || code != 0 {
				t.Fatalf("exit %d: %v", code, err)
			}
			if p.PID() == 0 {
				t.Fatal("no process identity")
			}
		})
	}
}

func controlConnection(t *testing.T, address string) {
	t.Helper()
	c, err := net.DialTimeout("tcp4", address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// EOF proves the accept loop consumed this connection, so its count is final.
	if _, err := io.ReadAll(c); err != nil {
		t.Fatal(err)
	}
}

func checkJob(t *testing.T, p *Process, plan Plan) {
	t.Helper()
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err != nil {
		t.Fatal(err)
	}
	expected := uint32(windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS | windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_PROCESS_MEMORY)
	if limits.BasicLimitInformation.LimitFlags != expected || limits.BasicLimitInformation.ActiveProcessLimit != 1 || uint64(limits.ProcessMemoryLimit) != plan.MemoryBytes {
		t.Fatalf("incorrect job limits: %+v", limits)
	}
	var cpu struct{ Flags, Rate uint32 }
	if err := windows.QueryInformationJobObject(p.job, windows.JobObjectCpuRateControlInformation, uintptr(unsafe.Pointer(&cpu)), uint32(unsafe.Sizeof(cpu)), nil); err != nil {
		t.Fatal(err)
	}
	if cpu.Flags != 5 || cpu.Rate != plan.CPUPercent*100 {
		t.Fatalf("incorrect CPU limit: %+v", cpu)
	}
}

func TestNativeLaunchRejections(t *testing.T) {
	root, exe := fixture(t)
	i, storage := identity(t, Runtime, root, exe)
	a, b, err := transport.PipePair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	base := Request{Plan: DefaultPlan(Runtime), Identity: i, Executable: exe, Storage: storage, Pipes: []windows.Handle{windows.Handle(b.Fd())}}
	for name, change := range map[string]func(*Request){
		"unknown":             func(r *Request) { r.Plan.Role = "other" },
		"foreign-identity":    func(r *Request) { r.Plan.Role = Writer },
		"missing-identity":    func(r *Request) { r.Identity = nil },
		"no-pipes":            func(r *Request) { r.Pipes = nil },
		"duplicate-pipes":     func(r *Request) { r.Pipes = append(r.Pipes, r.Pipes[0]) },
		"process-handle":      func(r *Request) { r.Pipes = []windows.Handle{windows.CurrentProcess()} },
		"invalid-handle":      func(r *Request) { r.Pipes = []windows.Handle{0} },
		"relative-executable": func(r *Request) { r.Executable = "probe.exe" },
		"stream":              func(r *Request) { r.Executable += ":stream" },
		"foreign-storage":     func(r *Request) { r.Storage = root },
		"nul-argument":        func(r *Request) { r.Arguments = []string{"bad\x00argument"} },
		"large-command":       func(r *Request) { r.Arguments = make([]string, 33) },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			change(&r)
			p, e := Launch(context.Background(), r)
			if e == nil || p != nil {
				if p != nil {
					_ = p.Close()
				}
				t.Fatalf("accepted invalid request: %v", e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := Launch(ctx, base); !errors.Is(err, context.Canceled) || p != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := i.ProvisionDirectory(storage); err == nil {
		t.Fatal("reused storage")
	}
	link := filepath.Join(root, "alias.exe")
	if err := os.Link(exe, link); err != nil {
		t.Fatal(err)
	}
	if held, err := lockPath(exe, false, nil); err == nil {
		closeHandles(held)
		t.Fatal("accepted hardlink")
	}
}

func TestNativeCloseAndCancellation(t *testing.T) {
	root, exe := fixture(t)
	i, storage := identity(t, Runtime, root, exe)
	a, b, err := transport.PipePair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	p, err := Launch(context.Background(), Request{Plan: DefaultPlan(Runtime), Identity: i, Executable: exe, Storage: storage, Pipes: []windows.Handle{windows.Handle(b.Fd())}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Close() }()
	_ = b.Close()
	if err := a.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(a).Encode(probeInput{Hang: true}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(context.Background()); err == nil {
		t.Fatal("wait after close")
	}
	if _, err := io.ReadAll(a); err != nil && !errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		t.Fatal(err)
	}
}

func TestNativeSuspendedFailureAndInheritance(t *testing.T) {
	root, exe := fixture(t)
	i, storage := identity(t, Runtime, root, exe)
	a, b, err := transport.PipePair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	sentinel, err := windows.CreateEvent(&sa, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.CloseHandle(sentinel) }()
	failure := errors.New("injected before resume")
	var child windows.Handle
	request := Request{Plan: DefaultPlan(Runtime), Identity: i, Executable: exe, Storage: storage, Pipes: []windows.Handle{windows.Handle(b.Fd())}}
	p, err := launch(context.Background(), request, func(process windows.Handle) error {
		// Keep a query/synchronize handle to verify termination on the error path.
		if err := windows.DuplicateHandle(windows.CurrentProcess(), process, windows.CurrentProcess(), &child, windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0); err != nil {
			t.Fatal(err)
		}
		var duplicate windows.Handle
		if err := windows.DuplicateHandle(process, sentinel, windows.CurrentProcess(), &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err == nil {
			defer func() { _ = windows.CloseHandle(duplicate) }()
			compare := windows.NewLazySystemDLL("kernel32.dll").NewProc("CompareObjectHandles")
			same, _, _ := compare.Call(uintptr(sentinel), uintptr(duplicate))
			if same != 0 {
				t.Error("unlisted inheritable handle reached child")
			}
		}
		return failure
	})
	if p != nil || !errors.Is(err, failure) {
		t.Fatalf("fault result %v %v", p, err)
	}
	if child == 0 {
		t.Fatal("did not reach suspended check")
	}
	defer func() { _ = windows.CloseHandle(child) }()
	if status, err := windows.WaitForSingleObject(child, 5000); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("suspended child leaked: %d %v", status, err)
	}
	_ = b.Close()
	if err := a.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(a)
	if len(data) != 0 || err != nil && !errors.Is(err, windows.ERROR_BROKEN_PIPE) {
		t.Fatalf("child ran before restrictions: %q %v", data, err)
	}
}

func TestNativeObjectRejections(t *testing.T) {
	root, exe := fixture(t)
	for _, value := range []struct {
		role Role
		name string
	}{{Network, "test"}, {Runtime, ""}, {Runtime, "../escape"}, {"other", "test"}, {Writer, "UPPER"}} {
		if i, err := NewIdentity(value.role, value.name); err == nil || i != nil {
			if i != nil {
				_ = i.Close()
			}
			t.Fatalf("accepted %+v", value)
		}
	}
	i, storage := identity(t, Runtime, root, exe)
	a, b, err := transport.PipePair()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close(); _ = b.Close() }()
	if handles, err := duplicatePipes([]windows.Handle{windows.Handle(a.Fd())}); err == nil {
		closeHandles(handles)
		t.Fatal("accepted pipe server/impersonation authority")
	}
	for _, handle := range []windows.Handle{0, windows.InvalidHandle, windows.Handle(windows.GetCurrentProcessToken())} {
		if handles, err := duplicatePipes([]windows.Handle{handle}); err == nil {
			closeHandles(handles)
			t.Fatal("accepted non-pipe")
		}
	}
	file, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if handles, err := duplicatePipes([]windows.Handle{windows.Handle(file.Fd())}); err == nil {
		closeHandles(handles)
		t.Fatal("accepted file")
	}
	// A real Winsock socket must not pass FILE_TYPE_PIPE validation.
	socket, err := windows.Socket(windows.AF_INET, windows.SOCK_STREAM, windows.IPPROTO_TCP)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = windows.Closesocket(socket) }()
	if handles, err := duplicatePipes([]windows.Handle{socket}); err == nil {
		closeHandles(handles)
		t.Fatal("accepted Winsock")
	}
	// Hold parents through use: a concurrent rename cannot replace the executable.
	held, err := lockPath(exe, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe, exe+".moved"); err == nil {
		closeHandles(held)
		t.Fatal("replaced held executable")
	}
	closeHandles(held)
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(root, "junction")
	if output, err := exec.Command(filepath.Join(system, "cmd.exe"), "/d", "/c", "mklink", "/J", junction, storage).CombinedOutput(); err != nil { //nolint:gosec // Fixed native test command and temporary paths.
		t.Fatalf("create test junction: %v: %s", err, output)
	}
	if held, err := lockPath(junction, true, i.sid); err == nil {
		closeHandles(held)
		t.Fatal("accepted junction")
	}
	// Broad read grants are rejected too: C's target must not be readable by R.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(storage, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if held, err := lockPath(storage, true, i.sid); err == nil {
		closeHandles(held)
		t.Fatal("accepted broad ACL")
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := i.Close(); err != nil {
		t.Fatal(err)
	}
	if err := i.ProvisionDirectory(filepath.Join(root, "closed")); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if err := i.GrantExecutable(exe); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
