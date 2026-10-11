//go:build linux

package network

import (
	"context"
	"net"
	"os"
	"strconv"
	"syscall"
	"testing"

	"github.com/asciimoth/almagest/bin/internal/isolation/transport"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"golang.org/x/sys/unix"
)

func TestLinuxMatcherParity(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Skip("native matcher checks require Docker")
	}
	s, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{MatcherRules: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeMatcherParity(t, s, "pid")
}

func TestLinuxMatcherSharedBind(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Skip("native matcher checks require Docker")
	}
	s, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{MatcherRules: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b, _ := testBroker(t)
	b.backend, err = NewSystemBackend(s, CaptureNativeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	c := startClient(t, b)
	rule := sysnet.Rule{Type: "pid", Rule: strconv.Itoa(os.Getpid())}
	direct, err := s.BuildMatcher(rule)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.Close() }()
	remote, err := c.BuildMatcher(rule)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = remote.Close() }()
	lc := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var sockErr error
		err := raw.Control(func(fd uintptr) { sockErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1) })
		if err != nil {
			return err
		}
		return sockErr
	}}
	first, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := lc.ListenPacket(context.Background(), "udp4", first.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	flow := sockowner.FlowTuple{Proto: "udp", LocalIP: net.IPv4(127, 0, 0, 1), LocalPort: uint16(first.LocalAddr().(*net.UDPAddr).Port), RemoteIP: net.IPv4(127, 0, 0, 1), RemotePort: 53}
	for range 2 {
		want, wantErr := direct.Match(flow)
		got, gotErr := remote.Match(flow)
		if got != want || transport.ErrorCode(wantErr) != transport.ErrorCode(gotErr) {
			t.Fatalf("shared bind: native=%v/%v RPC=%v/%v", want, wantErr, got, gotErr)
		}
		_ = second.Close()
	}
}

func TestLinuxRuleParity(t *testing.T) {
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Skip("native matcher checks require Docker")
	}
	s, err := linuxsysnet.New(linuxsysnet.SystemConfig{Features: linuxsysnet.FeatureConfig{MatcherRules: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	nativeRuleParity(t, s)
}
