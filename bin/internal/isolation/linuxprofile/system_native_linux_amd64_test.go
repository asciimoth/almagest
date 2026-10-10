//go:build linux && amd64

package linuxprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gdns "github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sockowner"
	"github.com/asciimoth/gonnect/sysnet"
	linuxsysnet "github.com/asciimoth/sysnet-linux"
	"github.com/asciimoth/sysnet-linux/routing"
	"github.com/vishvananda/netlink"
)

// This test changes only the disposable container's DNS and network namespace.
// It uses the real native provider, not the optional file-helper component.
func TestNativeTrustedSystem(t *testing.T) {
	nativeEnvironment(t)
	if os.Getenv("ALMAGEST_PROFILE_TRUSTED_SYSTEM") != "1" {
		t.Skip("requires the dedicated trusted-backend container")
	}
	mode := os.Getenv("ALMAGEST_TRUSTED_SYSTEM_CHILD")
	if mode != "" {
		testTrustedSystemChild(t, mode)
		return
	}
	var reports []sysnet.CapabilityReport
	for _, mode := range []string{"direct", "prepared"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeTrustedSystem$", "-test.v")
		cmd.Env = append(os.Environ(), "ALMAGEST_TRUSTED_SYSTEM_CHILD="+mode)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, output)
		}
		found := false
		for _, line := range strings.Split(string(output), "\n") {
			if data, ok := strings.CutPrefix(line, "BACKEND_CAPABILITIES "); ok {
				var report sysnet.CapabilityReport
				if err := json.Unmarshal([]byte(data), &report); err != nil {
					t.Fatal(err)
				}
				reports = append(reports, report)
				found = true
			}
		}
		if !found {
			t.Fatal("missing backend capability report")
		}
		t.Logf("%s: native DNS, TUN, routing cleanup and process matchers passed", mode)
	}
	if len(reports) != 2 || !reflect.DeepEqual(reports[0], reports[1]) {
		t.Fatalf("preparation changed native capabilities: %+v", reports)
	}
}

func testTrustedSystemChild(t *testing.T, mode string) {
	t.Helper()
	if mode != "direct" && mode != "prepared" {
		t.Fatal("unknown backend mode")
	}
	if mode == "prepared" {
		state, err := os.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer state.Close() //nolint:errcheck
		if err := state.Chmod(0700); err != nil {
			t.Fatal(err)
		}
		cgroup, err := os.Open("/sys/fs/cgroup")
		if err != nil {
			t.Fatal(err)
		}
		defer cgroup.Close() //nolint:errcheck
		if err := PrepareBackend(state, cgroup, DefaultLimits()); err != nil {
			t.Fatal(err)
		}
	}
	original, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	// Docker owns this file; no host path is mounted into this test container.
	t.Cleanup(func() {
		if err := os.WriteFile("/etc/resolv.conf", original, 0644); err != nil {
			t.Error(err)
		}
	})
	const upstreamConfig = "# backend qualification\nnameserver 127.0.0.54\n"
	if err := os.WriteFile("/etc/resolv.conf", []byte(upstreamConfig), 0644); err != nil {
		t.Fatal(err)
	}
	upstream := newProfileDNS(t, [4]byte{203, 0, 113, 54})
	conn, err := net.ListenPacket("udp4", "127.0.0.54:53")
	if err != nil {
		t.Fatal(err)
	}
	server := gdns.NewServer(conn, upstream, nil)
	t.Cleanup(func() { _ = server.Close() })
	underlay := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "trusted-uplink"}}
	if err := netlink.LinkAdd(underlay); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(underlay) })
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: underlay.Attrs().Index, Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}}); err != nil {
		t.Fatal(err)
	}
	beforeRules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		t.Fatal(err)
	}
	beforeRoutes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		t.Fatal(err)
	}
	system, err := linuxsysnet.New(linuxsysnet.SystemConfig{
		RoutingMonitorNamespace: routing.CurrentNamespace,
		DNS:                     linuxsysnet.DNSConfig{Mode: linuxsysnet.DNSModeDirect},
		Logf:                    t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = system.Close() })
	capabilities, err := json.Marshal(system.Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("BACKEND_CAPABILITIES %s\n", capabilities)
	regular, err := system.BuildTun(sysnet.TunOpts{Name: "trusted-regular", MTU: 1400, TunAddrs: []string{"198.18.1.1/30"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := regular.Close(); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"198.18.2.1", "198.18.3.1"} {
		device, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{TunAddrs: []string{address + "/32"}, DnsIP: address, MTU: 1400})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = device.Close() })
		name, err := device.Name()
		if err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		for mark, expected := range map[uint32]int{0: link.Attrs().Index, routing.DefaultAppBypassMark: underlay.Attrs().Index} {
			routes, err := netlink.RouteGetWithOptions(net.ParseIP("203.0.113.1"), &netlink.RouteGetOptions{Mark: mark})
			if err != nil || len(routes) != 1 || routes[0].LinkIndex != expected {
				t.Fatalf("default TUN route mark=%d: %v, %v", mark, routes, err)
			}
		}
		managed, err := os.ReadFile("/etc/resolv.conf")
		if err != nil || !bytes.Contains(managed, []byte("nameserver "+address)) {
			t.Fatalf("native DNS not installed: %s, %v", managed, err)
		}
		attached := newProfileDNS(t, [4]byte{203, 0, 113, 99})
		if err := device.SetDNS(attached); err != nil {
			t.Fatal(err)
		}
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(address, "53"))
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ips, err := resolver.LookupIP(ctx, "ip4", "attached.example.")
		cancel()
		if err != nil || len(ips) != 1 || ips[0].String() != "203.0.113.99" {
			t.Fatalf("attached backend DNS: %v, %v", ips, err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 3*time.Second)
		ips, err = gdns.NewResolver(system.OutDNS()).LookupIP(ctx, "ip4", "upstream.example.")
		cancel()
		if err != nil || len(ips) != 1 || ips[0].String() != "203.0.113.54" {
			t.Fatalf("OutDNS did not preserve original upstream: %v, %v", ips, err)
		}
		rebuilt, err := system.BuildDefaultTun(sysnet.DefaultTunOpts{TunAddrs: []string{address + "/32"}, DnsIP: address, MTU: 1500})
		if err != nil || rebuilt != device {
			t.Fatalf("dynamic default TUN changed public identity: %v", err)
		}
		if mtu, err := device.MTU(); err != nil || mtu != 1500 {
			t.Fatalf("dynamic default TUN MTU: %d, %v", mtu, err)
		}
		if err := device.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := netlink.LinkByName(name); err == nil {
			t.Fatal("default TUN remains after close")
		}
		restored, err := os.ReadFile("/etc/resolv.conf")
		if err != nil || string(restored) != upstreamConfig {
			t.Fatalf("DNS restore: %s, %v", restored, err)
		}
	}
	testTrustedProcessMatchers(t, system)
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}
	afterRules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil || !reflect.DeepEqual(beforeRules, afterRules) {
		t.Fatalf("routing rules not restored: before=%v after=%v error=%v", beforeRules, afterRules, err)
	}
	afterRoutes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil || !reflect.DeepEqual(beforeRoutes, afterRoutes) {
		t.Fatalf("routes not restored: before=%v after=%v error=%v", beforeRoutes, afterRoutes, err)
	}
}

// This checks native socket-owner lookup and procfs-based rules. It does not
// substitute for the separate kernel p-mark lifecycle qualification.
func testTrustedProcessMatchers(t *testing.T, system sysnet.System) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	comm, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Fatal(err)
	}
	rules := []sysnet.Rule{
		{Type: "pid", Rule: strconv.Itoa(os.Getpid())},
		{Type: "uid", Rule: strconv.Itoa(os.Getuid())},
		{Type: "gid", Rule: strconv.Itoa(os.Getgid())},
		{Type: "exec", Rule: executable},
		{Type: "comm", Rule: "^" + regexp.QuoteMeta(strings.TrimSpace(string(comm))) + "$"},
		{Type: "cmd", Rule: regexp.QuoteMeta("TestNativeTrustedSystem")},
	}
	for _, host := range []string{"127.0.0.1", "::1"} {
		listener, err := system.LocalNet().ListenTCP(context.Background(), "tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		accepted, err := listener.Accept()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = accepted.Close() })
		flow, err := sockowner.IncomingConnPeerFlow(accepted)
		if err != nil {
			t.Fatal(err)
		}
		packet, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = packet.Close() })
		udpFlow := sockowner.FlowTuple{Proto: "udp", LocalIP: net.ParseIP(host), LocalPort: uint16(packet.LocalAddr().(*net.UDPAddr).Port), RemoteIP: net.ParseIP(host), RemotePort: 53}
		for _, rule := range append(rules, sysnet.Rule{Type: "pid", Rule: "2147483647"}) {
			matcher, err := system.BuildMatcher(rule)
			if err != nil {
				t.Fatal(err)
			}
			for _, tuple := range []sockowner.FlowTuple{*flow, udpFlow} {
				matched, err := matcher.Match(tuple)
				want := rule.Rule != "2147483647"
				if err != nil || matched != want {
					t.Fatalf("%s %s %s: matched=%v want=%v error=%v", host, tuple.Proto, rule.Type, matched, want, err)
				}
			}
			if err := matcher.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type profileDNS struct {
	requests chan gdns.Request
	stop     chan struct{}
	once     sync.Once
}

func (d *profileDNS) Requests() chan<- gdns.Request { return d.requests }
func (d *profileDNS) Close() error                  { d.once.Do(func() { close(d.stop) }); return nil }

func newProfileDNS(t *testing.T, answer [4]byte) *profileDNS {
	t.Helper()
	d := &profileDNS{requests: make(chan gdns.Request), stop: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-d.stop:
				return
			case request := <-d.requests:
				message := &gdns.Message{ID: request.Message.ID, Response: true, RecursionAvailable: true, Questions: request.Message.Questions}
				for _, question := range message.Questions {
					if question.Type == gdns.TypeA {
						message.Answers = append(message.Answers, gdns.Resource{Name: question.Name, Type: gdns.TypeA, Class: gdns.ClassIN, TTL: 60, Data: append([]byte(nil), answer[:]...)})
					}
				}
				select {
				case request.Reply <- gdns.Response{Message: message}:
				case <-request.Context.Done():
				case <-d.stop:
					return
				}
			}
		}
	}()
	t.Cleanup(func() { _ = d.Close(); <-done })
	return d
}
