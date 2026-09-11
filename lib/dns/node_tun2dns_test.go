package dnsnodes_test

import (
	"encoding/binary"
	"strings"
	"testing"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	tunnodes "github.com/asciimoth/almagest/lib/tun"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTun2DnsPortsAndInitialLabel(t *testing.T) {
	e, _ := newTun2DNSTestE2E(t)
	bridge := e.AddNode(dnsnodes.NodeTypeTun2Dns, "bridge")

	snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, bridge)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", bridge)
	}
	if len(snapshot.LeftPorts) != 1 || len(snapshot.RightPorts) != 1 {
		t.Fatalf("Tun2Dns ports = left %v right %v, want one of each", snapshot.LeftPorts, snapshot.RightPorts)
	}
	assertPortShape(t, e.NodeE2E, snapshot.LeftPorts[0], "left", "Tun", links.TypeTun)
	assertPortShape(t, e.NodeE2E, snapshot.RightPorts[0], "right", "DNS", dnsnodes.TypeDNS)
	e.AssertNodeLabel(bridge, "waiting for TUN")
}

func TestTun2DnsAdaptsPipeTunPacketsToStaticDNS(t *testing.T) {
	e, providers := newTun2DNSTestE2E(t)
	provider := e.AddNode(testTunProviderNodeType, "tun")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	bridge := e.AddNode(dnsnodes.NodeTypeTun2Dns, "bridge")
	e.SubscribeMenus(static)
	e.applyStaticDNSRecords(static, "pipe.tun2dns.test A 10.144.0.53")

	e.MustAddTypedLinkByPortName(links.TypeTun, bridge, "left", "Tun", provider, "right", "Tun")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", bridge, "right", "DNS")
	e.WaitNodeLabel(bridge, "adapting DNS")

	peer := providers.peer(t, provider)
	resp := queryTun2DNSPeer(t, peer, "pipe.tun2dns.test.")
	if got := dnsnodes.FormatMessage(resp); !strings.Contains(got, "10.144.0.53") {
		t.Fatalf("DNS response = %q, want A 10.144.0.53", got)
	}
}

func TestTun2DnsReattachesDNSWithoutReplacingTun(t *testing.T) {
	e, providers := newTun2DNSTestE2E(t)
	provider := e.AddNode(testTunProviderNodeType, "tun")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	bridge := e.AddNode(dnsnodes.NodeTypeTun2Dns, "bridge")
	e.SubscribeMenus(static)
	e.applyStaticDNSRecords(static, "reattach.tun2dns.test A 10.145.0.53")

	e.MustAddTypedLinkByPortName(links.TypeTun, bridge, "left", "Tun", provider, "right", "Tun")
	dnsLink := e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", bridge, "right", "DNS")
	e.WaitNodeLabel(bridge, "adapting DNS")
	peer := providers.peer(t, provider)
	first := queryTun2DNSPeer(t, peer, "reattach.tun2dns.test.")
	if got := dnsnodes.FormatMessage(first); !strings.Contains(got, "10.145.0.53") {
		t.Fatalf("first DNS response = %q, want A 10.145.0.53", got)
	}

	e.Engine.RemoveLink(E2EWorkspace, dnsLink)
	e.WaitNodeLabel(bridge, "waiting for DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", bridge, "right", "DNS")
	e.WaitNodeLabel(bridge, "adapting DNS")

	second := queryTun2DNSPeer(t, peer, "reattach.tun2dns.test.")
	if got := dnsnodes.FormatMessage(second); !strings.Contains(got, "10.145.0.53") {
		t.Fatalf("second DNS response = %q, want A 10.145.0.53", got)
	}
}

func TestTun2DnsRequestsReplacementAfterTerminalTunRead(t *testing.T) {
	e, providers := newTun2DNSTestE2E(t)
	provider := e.AddNode(testTunProviderNodeType, "tun")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	bridge := e.AddNode(dnsnodes.NodeTypeTun2Dns, "bridge")
	e.SubscribeMenus(static)
	e.applyStaticDNSRecords(static, "recover.tun2dns.test A 10.147.0.53")

	tunLink := e.MustAddTypedLinkByPortName(links.TypeTun, bridge, "left", "Tun", provider, "right", "Tun")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", bridge, "right", "DNS")
	e.WaitNodeLabel(bridge, "adapting DNS")

	requestsBefore := providers.requestCount(t, provider)
	providers.ignoreRequests(t, provider, 2)
	providers.closeOutput(t, provider, tunLink)
	Eventually(t, func() bool {
		return providers.requestCount(t, provider) >= requestsBefore+3
	}, "Tun2Dns did not request a replacement TUN")
	e.WaitNodeLabel(bridge, "adapting DNS")

	resp := queryTun2DNSPeer(t, providers.peer(t, provider), "recover.tun2dns.test.")
	if got := dnsnodes.FormatMessage(resp); !strings.Contains(got, "10.147.0.53") {
		t.Fatalf("DNS response after TUN replacement = %q, want A 10.147.0.53", got)
	}
}

func TestTun2DnsE2ETunMiddlewareAndDNSCache(t *testing.T) {
	e, providers := newTun2DNSTestE2EWithDNSCache(t)
	provider := e.AddNode(testTunProviderNodeType, "provider")
	stripper := e.AddNode(tunnodes.NodeTypeTunConfigStripper, "stripper")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "static")
	cache := e.AddNode(dnsnodes.NodeTypeDnsCache, "cache")
	bridge := e.AddNode(dnsnodes.NodeTypeTun2Dns, "bridge")
	e.SubscribeMenus(static, cache)

	e.applyStaticDNSRecords(static, "cached.tun2dns.test A 10.146.0.80")

	e.MustAddTypedLinkByPortName(links.TypeTun, stripper, "left", "Tun", provider, "right", "Tun")
	e.MustAddTypedLinkByPortName(links.TypeTun, bridge, "left", "Tun", stripper, "right", "Tun")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", cache, "right", "DNS")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, cache, "left", "DNS", bridge, "right", "DNS")

	e.WaitNodeLabel(bridge, "adapting DNS")
	e.WaitNodeLabel(cache, "caching DNS")

	peer := providers.peer(t, provider)
	resp := queryTun2DNSPeer(t, peer, "cached.tun2dns.test.")
	if got := dnsnodes.FormatMessage(resp); !strings.Contains(got, "10.146.0.80") {
		t.Fatalf("DNS response = %q, want A 10.146.0.80", got)
	}
}

func TestTun2DnsSaveRestorePreservesLinks(t *testing.T) {
	e, providers := newTun2DNSTestE2E(t)
	provider := e.AddNode(testTunProviderNodeType, "restore-provider")
	stripper := e.AddNode(tunnodes.NodeTypeTunConfigStripper, "restore-stripper")
	static := e.AddNode(dnsnodes.NodeTypeStaticDNS, "restore-static")
	bridge := e.AddNode(dnsnodes.NodeTypeTun2Dns, "restore-bridge")
	e.SubscribeMenus(static)

	e.applyStaticDNSRecords(static, "restore.tun2dns.test A 10.148.0.90")
	e.MustAddTypedLinkByPortName(links.TypeTun, stripper, "left", "Tun", provider, "right", "Tun")
	e.MustAddTypedLinkByPortName(links.TypeTun, bridge, "left", "Tun", stripper, "right", "Tun")
	e.MustAddTypedLinkByPortName(dnsnodes.TypeDNS, static, "left", "DNS", bridge, "right", "DNS")
	e.WaitNodeLabel(bridge, "adapting DNS")
	resp := queryTun2DNSPeer(t, providers.peer(t, provider), "restore.tun2dns.test.")
	if got := dnsnodes.FormatMessage(resp); !strings.Contains(got, "10.148.0.90") {
		t.Fatalf("DNS response = %q, want A 10.148.0.90", got)
	}

	cfg := configer.NewMemory(nil)
	if err := e.Engine.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	restored, restoredProviders := newTun2DNSTestE2EFromConfig(t, cfg)
	restoredBridge := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "restore-bridge")
	restored.WaitNodeLabel(restoredBridge, "adapting DNS")
	restoredProvider := testkit.NodeIDByName(t, restored.Engine, E2EWorkspace, "restore-provider")
	restoredResp := queryTun2DNSPeer(t, restoredProviders.peer(t, restoredProvider), "restore.tun2dns.test.")
	if got := dnsnodes.FormatMessage(restoredResp); !strings.Contains(got, "10.148.0.90") {
		t.Fatalf("restored DNS response = %q, want A 10.148.0.90", got)
	}
}

func newTun2DNSTestE2E(t *testing.T) (*dnsE2E, *testTunProviderRegistry) {
	t.Helper()
	return newTun2DNSTestE2EFromConfig(t, configer.NewMemory(nil))
}

func newTun2DNSTestE2EWithDNSCache(t *testing.T) (*dnsE2E, *testTunProviderRegistry) {
	t.Helper()
	return newTun2DNSTestE2EWithConfig(t, configer.NewMemory(nil), newMemoryDNSCacheManager())
}

func newTun2DNSTestE2EFromConfig(t *testing.T, cfg configer.Config) (*dnsE2E, *testTunProviderRegistry) {
	t.Helper()
	return newTun2DNSTestE2EWithConfig(t, cfg, nil)
}

func newTun2DNSTestE2EWithConfig(t *testing.T, cfg configer.Config, manager storage.DnsCacheManager) (*dnsE2E, *testTunProviderRegistry) {
	t.Helper()
	providers := newTestTunProviderRegistry()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newE2EDebugSystem()
	if cfg == nil {
		cfg = configer.NewMemory(nil)
	}
	engine, err := BuildEngine(
		t.Context(),
		cfg,
		logf,
		sys,
		nil,
		manager, nil,
		nil,

		[]pasta.NodeClass{testTunProviderClass{registry: providers}},
		pool,
		true)

	if err != nil {
		t.Fatalf("BuildEngine: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("engine.Close error = %v, want nil", err)
		}
	})
	e := NewNodeE2EFromEngine(t, engine, logf, sys)
	return newDNSE2EFromNodeE2E(e), providers
}

func queryTun2DNSPeer(t *testing.T, peer tun.Tun, name string) *dns.Message {
	t.Helper()
	req := dnsTestQuery(name, dns.TypeA)
	packet := tun2DNSTestIPv4DNSPacket(t, [4]byte{10, 0, 0, 2}, [4]byte{10, 0, 0, 53}, 53000, 53, req)
	writeTunPacket(t, peer, packet)
	respPacket := readTunPacket(t, peer)
	_, _, srcPort, dstPort, resp := parseTun2DNSTestIPv4DNSResponse(t, respPacket)
	if srcPort != 53 || dstPort != 53000 {
		t.Fatalf("response ports = %d/%d, want 53/53000", srcPort, dstPort)
	}
	if resp.ID != req.ID || !resp.Response || resp.RCode != dns.RCodeSuccess {
		t.Fatalf("DNS response = %#v, want successful response for id %d", resp, req.ID)
	}
	return resp
}

func tun2DNSTestIPv4DNSPacket(t *testing.T, src, dst [4]byte, srcPort, dstPort uint16, msg *dns.Message) []byte {
	t.Helper()
	payload, err := dns.Pack(msg)
	if err != nil {
		t.Fatalf("dns.Pack: %v", err)
	}
	udpLen := 8 + len(payload)
	totalLen := 20 + udpLen
	packet := make([]byte, totalLen)
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(totalLen))
	packet[8] = 64
	packet[9] = 17
	copy(packet[12:16], src[:])
	copy(packet[16:20], dst[:])
	binary.BigEndian.PutUint16(packet[20:22], srcPort)
	binary.BigEndian.PutUint16(packet[22:24], dstPort)
	binary.BigEndian.PutUint16(packet[24:26], uint16(udpLen))
	copy(packet[28:], payload)
	checksum := tun2DNSTestUDPChecksumIPv4(src, dst, packet[20:])
	if checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(packet[26:28], checksum)
	return packet
}

func parseTun2DNSTestIPv4DNSResponse(t *testing.T, packet []byte) ([4]byte, [4]byte, uint16, uint16, *dns.Message) {
	t.Helper()
	if len(packet) < 28 || packet[0]>>4 != 4 {
		t.Fatalf("response packet is not IPv4 UDP: %x", packet)
	}
	headerLen := int(packet[0]&0x0f) * 4
	if headerLen < 20 || len(packet) < headerLen+8 {
		t.Fatalf("response packet has invalid IPv4 header length %d", headerLen)
	}
	var src, dst [4]byte
	copy(src[:], packet[12:16])
	copy(dst[:], packet[16:20])
	srcPort := binary.BigEndian.Uint16(packet[headerLen : headerLen+2])
	dstPort := binary.BigEndian.Uint16(packet[headerLen+2 : headerLen+4])
	udpLen := int(binary.BigEndian.Uint16(packet[headerLen+4 : headerLen+6]))
	if udpLen < 8 || headerLen+udpLen > len(packet) {
		t.Fatalf("response UDP length %d invalid for packet length %d", udpLen, len(packet))
	}
	msg, err := dns.Unpack(packet[headerLen+8 : headerLen+udpLen])
	if err != nil {
		t.Fatalf("dns.Unpack response: %v", err)
	}
	return src, dst, srcPort, dstPort, msg
}

func tun2DNSTestUDPChecksumIPv4(src, dst [4]byte, udp []byte) uint16 {
	pseudo := make([]byte, 0, 12+len(udp)+1)
	pseudo = append(pseudo, src[:]...)
	pseudo = append(pseudo, dst[:]...)
	pseudo = append(pseudo, 0, 17)
	pseudo = binary.BigEndian.AppendUint16(pseudo, uint16(len(udp)))
	pseudo = append(pseudo, udp...)
	return tun2DNSTestChecksum(pseudo)
}

func tun2DNSTestChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) > 1 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
