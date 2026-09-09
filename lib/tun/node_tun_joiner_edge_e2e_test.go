package tunnodes_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/asciimoth/gonnect/tun"
)

const (
	testIPProtocolICMPv4 = 1
	testIPProtocolTCP    = 6
	testIPProtocolUDP    = 17
	testIPProtocolICMPv6 = 58
)

type tunJoinerPacketE2EFixture struct {
	t          *testing.T
	e          *NodeE2E
	providers  *testTunProviderRegistry
	sinks      *testTunSinkRegistry
	joiner     uint64
	outputLink uint64
	output     tun.Tun
}

func newTunJoinerPacketE2EFixture(t *testing.T) *tunJoinerPacketE2EFixture {
	t.Helper()
	e, providers, sinks := newTunJoinerE2E(t)
	joiner := e.AddNode(NodeTypeTunJoiner, "joiner")
	sink := e.AddNode(testTunSinkNodeType, "sink")
	outputLink := e.MustAddTypedLinkByPortName(TypeTun, sink, "left", "Tun", joiner, "right", "Tun")
	return &tunJoinerPacketE2EFixture{
		t:          t,
		e:          e,
		providers:  providers,
		sinks:      sinks,
		joiner:     joiner,
		outputLink: outputLink,
		output:     sinks.tun(t, sink),
	}
}

func (f *tunJoinerPacketE2EFixture) attachDefault(name string) tun.Tun {
	f.t.Helper()
	provider := f.e.AddNode(testTunProviderNodeType, name)
	f.e.MustAddTypedLinkByPortName(
		TypeTun,
		f.joiner,
		"left",
		"Default",
		provider,
		"right",
		"Tun",
	)
	return f.providers.peer(f.t, provider)
}

func (f *tunJoinerPacketE2EFixture) attachSecondary(name string) (uint64, tun.Tun) {
	f.t.Helper()
	provider := f.e.AddNode(testTunProviderNodeType, name)
	link := f.e.MustAddTypedLinkByPortName(
		TypeTun,
		f.joiner,
		"left",
		"Secondary",
		provider,
		"right",
		"Tun",
	)
	peer := f.providers.peer(f.t, provider)
	return link, peer
}

func (f *tunJoinerPacketE2EFixture) replaceOutput(name string) {
	f.t.Helper()
	f.e.Engine.RemoveLink(E2EWorkspace, f.outputLink)
	sink := f.e.AddNode(testTunSinkNodeType, name)
	f.outputLink = f.e.MustAddTypedLinkByPortName(
		TypeTun,
		sink,
		"left",
		"Tun",
		f.joiner,
		"right",
		"Tun",
	)
	f.output = f.sinks.tun(f.t, sink)
}

func TestTunJoinerE2ERoutesSharedIPv6AndICMPEchoFlows(t *testing.T) {
	f := newTunJoinerPacketE2EFixture(t)
	defaultPeer := f.attachDefault("default")
	_, secondaryPeer := f.attachSecondary("secondary")
	f.e.WaitNodeLabel(f.joiner, "joined default + 1 secondary")

	local4 := [4]byte{192, 0, 2, 10}
	remote4 := [4]byte{198, 51, 100, 20}
	local6 := netip.MustParseAddr("2001:db8:1::10").As16()
	remote6 := netip.MustParseAddr("2001:db8:2::20").As16()
	flows := []struct {
		name     string
		owner    tun.Tun
		outbound []byte
		reply    []byte
	}{
		{
			name:     "default IPv6 TCP",
			owner:    defaultPeer,
			outbound: testIPv6TransportPacket(local6, remote6, testIPProtocolTCP, 40001, 443),
			reply:    testIPv6TransportPacket(remote6, local6, testIPProtocolTCP, 443, 40001),
		},
		{
			name:     "default IPv4 ICMP echo",
			owner:    defaultPeer,
			outbound: testIPv4ICMPEchoPacket(local4, remote4, 8, 101),
			reply:    testIPv4ICMPEchoPacket(remote4, local4, 0, 101),
		},
		{
			name:     "secondary IPv6 UDP",
			owner:    secondaryPeer,
			outbound: testIPv6TransportPacket(local6, remote6, testIPProtocolUDP, 41001, 51820),
			reply:    testIPv6TransportPacket(remote6, local6, testIPProtocolUDP, 51820, 41001),
		},
		{
			name:     "secondary IPv6 ICMP echo",
			owner:    secondaryPeer,
			outbound: testIPv6ICMPEchoPacket(local6, remote6, 128, 202),
			reply:    testIPv6ICMPEchoPacket(remote6, local6, 129, 202),
		},
	}

	for _, flow := range flows {
		assertTunPacketTransferred(t, flow.owner, f.output, flow.outbound)
	}
	for _, flow := range flows {
		flow := flow
		t.Run(flow.name, func(t *testing.T) {
			assertTunPacketTransferred(t, f.output, flow.owner, flow.reply)
		})
	}
}

func TestTunJoinerE2ESecondaryFlowWorksBeforeDefaultAttachment(t *testing.T) {
	f := newTunJoinerPacketE2EFixture(t)
	_, secondaryPeer := f.attachSecondary("secondary")
	f.e.WaitNodeLabel(f.joiner, "waiting for default")

	sharedIP := [4]byte{192, 0, 2, 10}
	remoteIP := [4]byte{198, 51, 100, 20}
	secondaryFlow := testIPv4TransportPacket(sharedIP, remoteIP, testIPProtocolUDP, 41001, 51820)
	secondaryReply := testIPv4TransportPacket(remoteIP, sharedIP, testIPProtocolUDP, 51820, 41001)
	assertTunPacketTransferred(t, secondaryPeer, f.output, secondaryFlow)
	assertTunPacketTransferred(t, f.output, secondaryPeer, secondaryReply)

	defaultPeer := f.attachDefault("default-late")
	f.e.WaitNodeLabel(f.joiner, "joined default + 1 secondary")
	defaultFlow := testIPv4TransportPacket(sharedIP, remoteIP, testIPProtocolTCP, 40001, 443)
	defaultReply := testIPv4TransportPacket(remoteIP, sharedIP, testIPProtocolTCP, 443, 40001)
	assertTunPacketTransferred(t, defaultPeer, f.output, defaultFlow)
	assertTunPacketTransferred(t, secondaryPeer, f.output, secondaryFlow)
	assertTunPacketTransferred(t, f.output, defaultPeer, defaultReply)
	assertTunPacketTransferred(t, f.output, secondaryPeer, secondaryReply)
}

func TestTunJoinerE2EDetachClearsFlowAndReplacementCanClaimTuple(t *testing.T) {
	f := newTunJoinerPacketE2EFixture(t)
	defaultPeer := f.attachDefault("default")
	secondaryLink, secondaryPeer := f.attachSecondary("secondary")
	f.e.WaitNodeLabel(f.joiner, "joined default + 1 secondary")

	sharedIP := [4]byte{192, 0, 2, 10}
	remoteIP := [4]byte{198, 51, 100, 20}
	flow := testIPv4TransportPacket(sharedIP, remoteIP, testIPProtocolTCP, 41001, 443)
	reply := testIPv4TransportPacket(remoteIP, sharedIP, testIPProtocolTCP, 443, 41001)
	assertTunPacketTransferred(t, secondaryPeer, f.output, flow)
	assertTunPacketTransferred(t, f.output, secondaryPeer, reply)

	f.e.Engine.RemoveLink(E2EWorkspace, secondaryLink)
	f.e.WaitNodeLabel(f.joiner, "joined default")
	assertTunPacketTransferred(t, f.output, defaultPeer, reply)

	_, replacementPeer := f.attachSecondary("secondary-replacement")
	f.e.WaitNodeLabel(f.joiner, "joined default + 1 secondary")
	assertTunPacketTransferred(t, replacementPeer, f.output, flow)
	// The default input now sends the identical tuple last. The replacement
	// remains its first active owner until it detaches.
	assertTunPacketTransferred(t, defaultPeer, f.output, flow)
	assertTunPacketTransferred(t, f.output, replacementPeer, reply)
}

func TestTunJoinerE2EOutputReplacementPreservesInputFlowOwners(t *testing.T) {
	f := newTunJoinerPacketE2EFixture(t)
	defaultPeer := f.attachDefault("default")
	_, secondaryPeer := f.attachSecondary("secondary")
	f.e.WaitNodeLabel(f.joiner, "joined default + 1 secondary")

	sharedIP := [4]byte{192, 0, 2, 10}
	remoteIP := [4]byte{198, 51, 100, 20}
	defaultFlow := testIPv4TransportPacket(sharedIP, remoteIP, testIPProtocolTCP, 40001, 443)
	defaultReply := testIPv4TransportPacket(remoteIP, sharedIP, testIPProtocolTCP, 443, 40001)
	secondaryFlow := testIPv4TransportPacket(sharedIP, remoteIP, testIPProtocolUDP, 41001, 51820)
	secondaryReply := testIPv4TransportPacket(remoteIP, sharedIP, testIPProtocolUDP, 51820, 41001)

	assertTunPacketTransferred(t, defaultPeer, f.output, defaultFlow)
	assertTunPacketTransferred(t, secondaryPeer, f.output, secondaryFlow)
	f.replaceOutput("replacement sink")

	// Replacing the output consumer must not detach the input TUNs or clear
	// the flow ownership learned from them.
	assertTunPacketTransferred(t, f.output, defaultPeer, defaultReply)
	assertTunPacketTransferred(t, f.output, secondaryPeer, secondaryReply)
}

func TestTunJoinerE2EMixedOwnerBatchAcrossManySecondaries(t *testing.T) {
	f := newTunJoinerPacketE2EFixture(t)
	owners := []tun.Tun{f.attachDefault("default")}
	for i := 1; i <= 3; i++ {
		_, peer := f.attachSecondary(fmt.Sprintf("secondary-%d", i))
		owners = append(owners, peer)
	}
	f.e.WaitNodeLabel(f.joiner, "joined default + 3 secondary")

	sharedIP := [4]byte{192, 0, 2, 10}
	remoteIP := [4]byte{198, 51, 100, 20}
	outboundByOwner := make([][][]byte, len(owners))
	repliesByOwner := make([][][]byte, len(owners))
	for ownerIndex := range owners {
		for flowIndex := 0; flowIndex < 4; flowIndex++ {
			protocol := uint8(testIPProtocolTCP)
			remotePort := uint16(443)
			if flowIndex%2 == 1 {
				protocol = testIPProtocolUDP
				remotePort = 51820
			}
			localPort := uint16(40000 + ownerIndex*100 + flowIndex)
			outboundByOwner[ownerIndex] = append(
				outboundByOwner[ownerIndex],
				testIPv4TransportPacket(sharedIP, remoteIP, protocol, localPort, remotePort),
			)
			repliesByOwner[ownerIndex] = append(
				repliesByOwner[ownerIndex],
				testIPv4TransportPacket(remoteIP, sharedIP, protocol, remotePort, localPort),
			)
		}
		for _, packet := range outboundByOwner[ownerIndex] {
			assertTunPacketTransferred(t, owners[ownerIndex], f.output, packet)
		}
	}

	mixedReplies := make([][]byte, 0, len(owners)*4)
	for flowIndex := 0; flowIndex < 4; flowIndex++ {
		for ownerIndex := len(owners) - 1; ownerIndex >= 0; ownerIndex-- {
			mixedReplies = append(mixedReplies, repliesByOwner[ownerIndex][flowIndex])
		}
	}
	reads := make([]<-chan tunPacketReadResult, len(owners))
	for ownerIndex := range owners {
		reads[ownerIndex] = readTunPacketsAsync(owners[ownerIndex], len(repliesByOwner[ownerIndex]))
	}
	writeTunPackets(t, f.output, mixedReplies)
	for ownerIndex := range owners {
		select {
		case result := <-reads[ownerIndex]:
			if result.err != nil {
				t.Fatalf("owner %d tun Read: %v", ownerIndex, result.err)
			}
			assertTunPacketSlicesEqual(t, result.packets, repliesByOwner[ownerIndex])
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for owner %d batch", ownerIndex)
		}
	}
}

func writeTunPackets(t *testing.T, target tun.Tun, packets [][]byte) {
	t.Helper()
	offset := target.MWO()
	bufs := make([][]byte, len(packets))
	for i, packet := range packets {
		bufs[i] = make([]byte, offset+len(packet))
		copy(bufs[i][offset:], packet)
	}
	n, err := target.Write(bufs, offset)
	if err != nil {
		t.Fatalf("tun batch Write: %v", err)
	}
	if n != len(packets) {
		t.Fatalf("tun batch Write count = %d, want %d", n, len(packets))
	}
}

type tunPacketReadResult struct {
	packets [][]byte
	err     error
}

func readTunPacketsAsync(source tun.Tun, count int) <-chan tunPacketReadResult {
	result := make(chan tunPacketReadResult, 1)
	go func() {
		packets := make([][]byte, 0, count)
		for range count {
			offset := source.MRO()
			buf := make([]byte, offset+2048)
			sizes := []int{0}
			n, err := source.Read([][]byte{buf}, sizes, offset)
			if err != nil {
				result <- tunPacketReadResult{err: err}
				return
			}
			if n != 1 {
				result <- tunPacketReadResult{err: fmt.Errorf("read packet count %d, want 1", n)}
				return
			}
			packets = append(packets, append([]byte(nil), buf[offset:offset+sizes[0]]...))
		}
		result <- tunPacketReadResult{packets: packets}
	}()
	return result
}

func assertTunPacketSlicesEqual(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("tun packet count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("tun packet %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func testIPv4ICMPEchoPacket(src, dst [4]byte, messageType uint8, identifier uint16) []byte {
	p := make([]byte, 28)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[2:4], uint16(len(p)))
	p[9] = testIPProtocolICMPv4
	copy(p[12:16], src[:])
	copy(p[16:20], dst[:])
	p[20] = messageType
	binary.BigEndian.PutUint16(p[24:26], identifier)
	return p
}

func testIPv6TransportPacket(src, dst [16]byte, protocol uint8, srcPort, dstPort uint16) []byte {
	p := make([]byte, 44)
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:6], uint16(len(p)-40))
	p[6] = protocol
	p[7] = 64
	copy(p[8:24], src[:])
	copy(p[24:40], dst[:])
	binary.BigEndian.PutUint16(p[40:42], srcPort)
	binary.BigEndian.PutUint16(p[42:44], dstPort)
	return p
}

func testIPv6ICMPEchoPacket(src, dst [16]byte, messageType uint8, identifier uint16) []byte {
	p := make([]byte, 48)
	p[0] = 0x60
	binary.BigEndian.PutUint16(p[4:6], uint16(len(p)-40))
	p[6] = testIPProtocolICMPv6
	p[7] = 64
	copy(p[8:24], src[:])
	copy(p[24:40], dst[:])
	p[40] = messageType
	binary.BigEndian.PutUint16(p[44:46], identifier)
	return p
}
