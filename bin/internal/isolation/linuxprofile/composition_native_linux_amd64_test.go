//go:build linux && amd64

package linuxprofile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/bin/internal/isolation/network"
	"github.com/asciimoth/almagest/bin/internal/isolation/policy"
	"github.com/asciimoth/almagest/bin/internal/isolation/protocol"
	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	gdns "github.com/asciimoth/gonnect/dns"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"github.com/asciimoth/sysnet-linux/routing"
	"github.com/vishvananda/netlink"
)

func TestNativeTrustedComposition(t *testing.T) {
	nativeEnvironment(t)
	if os.Getenv("ALMAGEST_PROFILE_TRUSTED_SYSTEM") != "1" {
		t.Skip("requires the dedicated trusted-backend container")
	}
	if os.Getenv("ALMAGEST_COMPOSITION_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeTrustedComposition$", "-test.v")
		cmd.Env = append(os.Environ(), "ALMAGEST_COMPOSITION_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("composition: %v\n%s", err, output)
		}
		t.Logf("%s", output)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	open := func(path string) *os.File {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		return f
	}
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	runtimeState := open(state)
	if err := os.Chown(state, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chown(state, 0, 0) })
	var parents, children []*os.File
	for range 3 {
		parent, child, err := transport.SocketPair()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = parent.Close(); _ = child.Close() })
		parents = append(parents, parent)
		children = append(children, child)
	}
	cgroup := open("/sys/fs/cgroup")
	cmd, err := CommandContext(ctx, Plan{Role: Runtime, UID: 65534, GID: 65534, Limits: DefaultLimits()}, Inputs{
		Launcher: open("/qualification/launcher"), Executable: open("/qualification/runtime-client-probe"), Directory: runtimeState, Cgroup: cgroup,
		Control: children[0], Data: children[1], Diagnostics: children[2],
	})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		_ = child.Close()
	}
	var waitErr error
	finished := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(finished) }()
	t.Cleanup(func() {
		select {
		case <-finished:
		default:
			_ = cmd.Process.Kill()
			<-finished
		}
		if waitErr != nil {
			t.Logf("runtime exit: %v\n%s", waitErr, output.Bytes())
		}
	})
	// The native launcher waits for its one PID-namespace child. Read that
	// relationship from trusted procfs, never from a runtime-supplied PID. The
	// inherited endpoint and the live launcher bind this identity until exit.
	var pid int64
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", cmd.Process.Pid, cmd.Process.Pid))
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 1 {
				pid, err = strconv.ParseInt(fields[0], 10, 32)
				if err == nil && pid > 0 {
					break
				}
			}
		}
		select {
		case <-finished:
			t.Fatalf("runtime exited before binding: %v\n%s", waitErr, output.Bytes())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pid <= 0 {
		t.Fatal("launcher child identity unavailable")
	}
	streams := make([]transport.Stream, 0, 3)
	for _, parent := range parents {
		stream, err := transport.BindSocket(parent, transport.LinuxPeer{PID: int32(pid), UID: 65534, GID: 65534})
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, stream)
		t.Cleanup(func() { _ = stream.Close() })
	}
	nativeState := open(t.TempDir())
	if err := nativeState.Chmod(0700); err != nil {
		t.Fatal(err)
	}
	if err := PrepareBackend(nativeState, cgroup, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	originalDNS, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile("/etc/resolv.conf", originalDNS, 0644) })
	const resolverFixture = "# composition DNS fixture\nnameserver 127.0.0.54\n"
	if err := os.WriteFile("/etc/resolv.conf", []byte(resolverFixture), 0644); err != nil {
		t.Fatal(err)
	}
	upstreamConn, err := net.ListenPacket("udp4", "127.0.0.54:53")
	if err != nil {
		t.Fatal(err)
	}
	upstream := gdns.NewServer(upstreamConn, newProfileDNS(t, [4]byte{203, 0, 113, 54}), nil)
	t.Cleanup(func() { _ = upstream.Close() })
	linkNames := func() []string {
		t.Helper()
		links, err := netlink.LinkList()
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(links))
		for _, link := range links {
			names = append(names, link.Attrs().Name)
		}
		slices.Sort(names)
		return names
	}
	originalLinks := linkNames()
	system, err := linuxsysnet.New(linuxsysnet.SystemConfig{RoutingMonitorNamespace: routing.CurrentNamespace, DNS: linuxsysnet.DNSConfig{Mode: linuxsysnet.DNSModeDirect}, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Close() })
	backend, err := network.NewSystemBackend(system, network.CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Default(policy.LaunchAuthority{Backend: "linux", CredentialClass: "container-root"}, policy.CommandAuthority{})
	digest, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	id, err := protocol.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{ID: id, Epoch: 1, Version: protocol.Version, PolicyDigest: digest}
	broker, err := network.New(session, p, backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = broker.Shutdown(context.Background()) })
	applicationDNS := network.NewApplicationDNSEndpoint()
	applicationDNSRef, err := broker.RegisterDNSEndpoint(ctx, 1, applicationDNS)
	if err != nil {
		t.Fatal(err)
	}
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = echo.Close() })
	if err := echo.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	echoDone := make(chan error, 1)
	go func() {
		packet := make([]byte, 128)
		n, peer, err := echo.ReadFrom(packet)
		if err == nil {
			_, err = echo.WriteTo(packet[:n], peer)
		}
		echoDone <- err
	}()
	var bootstrap bytes.Buffer
	if err := binary.Write(&bootstrap, binary.LittleEndian, session); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&bootstrap, binary.LittleEndian, uint16(echo.LocalAddr().(*net.UDPAddr).Port)); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&bootstrap, binary.LittleEndian, applicationDNSRef.ID); err != nil {
		t.Fatal(err)
	}
	if err := streams[0].SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := streams[0].Write(bootstrap.Bytes()); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() {
		served <- broker.Serve(ctx, transport.Lanes{Control: streams[0], Data: streams[1], Diagnostic: streams[2]})
	}()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if waitErr != nil {
		t.Fatalf("restricted runtime: %v\n%s", waitErr, output.Bytes())
	}
	if err := <-echoDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served: // Peer loss is expected; uncertain cleanup is not.
		if errors.Is(err, protocol.ErrRecoveryRequired) {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("broker did not finish after runtime exit")
	}
	if _, err := netlink.LinkByName("composed-tun"); err == nil {
		t.Fatal("runtime TUN leaked")
	}
	if finalLinks := linkNames(); !slices.Equal(originalLinks, finalLinks) {
		t.Fatalf("runtime leaked interfaces: before=%v after=%v", originalLinks, finalLinks)
	}
	finalDNS, err := os.ReadFile("/etc/resolv.conf")
	if err != nil || string(finalDNS) != resolverFixture {
		t.Fatalf("runtime default TUN did not restore native DNS: %v", err)
	}
	t.Logf("%s", output.Bytes())
}
