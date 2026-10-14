package amnesianodes

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	netstackvtun "github.com/asciimoth/gonnect-netstack/vtun"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
	wgoamnesia "github.com/asciimoth/wgo/amnesia"

	dnsnodes "github.com/asciimoth/almagest/lib/dns"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
)

func TestManagedAmnesiaLinksAttachDetachReattach(t *testing.T) {
	spawner := &managedAmnesiaLifecycleSpawner{}
	runtime := managedAmnesiaLifecycleRuntime{
		kv:      managedAmnesiaKVManager{store: newAmnesiaMemoryKV()},
		spawner: spawner,
	}
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class},
		managedAmnesiaLifecycleConfig("amnesia"),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	spawner.wait()
	t.Cleanup(func() {
		w.Close()
		spawner.wait()
	})
	node := class.onlyNode(t)

	tunProvider := &managedAmnesiaLinkPeer{}
	tunProviderID, err := w.AddNode(tunProvider, "test/AmnesiaTunProvider", "tun provider")
	if err != nil {
		t.Fatalf("add TUN provider: %v", err)
	}
	tunPort, err := w.AddPort(pasta.Port{
		Node: tunProviderID, Direction: "right", Name: "Tun", Types: []string{nodekit.TypeTun},
	})
	if err != nil {
		t.Fatalf("add TUN provider port: %v", err)
	}
	networkProvider := &managedAmnesiaLinkPeer{}
	networkProviderID, err := w.AddNode(networkProvider, "test/AmnesiaNetworkProvider", "network provider")
	if err != nil {
		t.Fatalf("add network provider: %v", err)
	}
	networkPort, err := w.AddPort(pasta.Port{
		Node: networkProviderID, Direction: "left", Name: "Network", Types: []string{nodekit.TypeNetwork},
	})
	if err != nil {
		t.Fatalf("add network provider port: %v", err)
	}

	firstTunLink, _, err := w.AddLink(tunPort, node.tunp)
	if err != nil {
		t.Fatalf("attach TUN link: %v", err)
	}
	firstNetworkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("attach network link: %v", err)
	}
	firstTun, firstTunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = firstTun.Close()
		_ = firstTunPeer.Close()
	})
	firstNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	firstMTU := links.NewLayer3DatagramMTUProvider(1500, true, false, links.MTUConstraint{Source: "first carrier"})
	w.EmitEvent(tunProviderID, firstTunLink, nodekit.TunPayload{Tun: firstTun})
	w.EmitEvent(networkProviderID, firstNetworkLink, nodekit.NetworkPayload{
		Network: firstNetwork, DatagramMTU: firstMTU, CapabilityRevision: firstMTU.Revision(),
	})
	spawner.wait()
	if node.tunLink != firstTunLink || node.tun != firstTun {
		t.Fatalf("attached TUN = link %d value %p, want link %d value %p", node.tunLink, node.tun, firstTunLink, firstTun)
	}
	if node.netLink != firstNetworkLink || node.network != firstNetwork {
		t.Fatalf("attached network = link %d value %p, want link %d value %p", node.netLink, node.network, firstNetworkLink, firstNetwork)
	}
	if !links.SameDatagramMTUProvider(node.networkPayload.DatagramMTU, firstMTU) ||
		node.networkPayload.CapabilityRevision != firstMTU.Revision() || node.unsubscribeNetworkMTU == nil {
		t.Fatal("first network did not attach its MTU capability")
	}

	w.RemoveLink(firstTunLink)
	w.RemoveLink(firstNetworkLink)
	spawner.wait()
	if node.tunLink != 0 || node.tun != nil || node.netLink != 0 || node.network != nil {
		t.Fatalf(
			"detached inputs = tun_link %d tun %p network_link %d network %p",
			node.tunLink, node.tun, node.netLink, node.network,
		)
	}
	if node.networkPayload.Network != nil || node.networkPayload.DatagramMTU != nil ||
		node.networkMTURevision != 0 || node.unsubscribeNetworkMTU != nil {
		t.Fatalf("detached MTU state was not cleared: %+v", node.networkPayload)
	}
	firstMTU.SetMTU(1400, true, false, links.MTUConstraint{Source: "stale carrier update"})
	spawner.wait()
	secondTunLink, _, err := w.AddLink(tunPort, node.tunp)
	if err != nil {
		t.Fatalf("reattach TUN link: %v", err)
	}
	secondNetworkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("reattach network link: %v", err)
	}
	secondTun, secondTunPeer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = secondTun.Close()
		_ = secondTunPeer.Close()
	})
	secondNetwork := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	secondMTU := links.NewLayer3DatagramMTUProvider(1350, true, false, links.MTUConstraint{Source: "second carrier"})
	w.EmitEvent(tunProviderID, secondTunLink, nodekit.TunPayload{Tun: secondTun})
	w.EmitEvent(networkProviderID, secondNetworkLink, nodekit.NetworkPayload{
		Network: secondNetwork, DatagramMTU: secondMTU, CapabilityRevision: secondMTU.Revision(),
	})
	spawner.wait()
	if secondTunLink == firstTunLink || secondNetworkLink == firstNetworkLink {
		t.Fatal("reattach reused a removed link ID")
	}
	if node.tunLink != secondTunLink || node.tun != secondTun {
		t.Fatalf("reattached TUN = link %d value %p, want link %d value %p", node.tunLink, node.tun, secondTunLink, secondTun)
	}
	if node.netLink != secondNetworkLink || node.network != secondNetwork {
		t.Fatalf("reattached network = link %d value %p, want link %d value %p", node.netLink, node.network, secondNetworkLink, secondNetwork)
	}
	if !links.SameDatagramMTUProvider(node.networkPayload.DatagramMTU, secondMTU) ||
		node.networkPayload.CapabilityRevision != secondMTU.Revision() || node.unsubscribeNetworkMTU == nil {
		t.Fatal("reattached network did not replace the MTU capability")
	}
	firstMTU.SetMTU(1300, true, false, links.MTUConstraint{Source: "second stale update"})
	spawner.wait()
	if !links.SameDatagramMTUProvider(node.networkPayload.DatagramMTU, secondMTU) ||
		node.networkPayload.CapabilityRevision != secondMTU.Revision() {
		t.Fatal("detached MTU provider changed the reattached network")
	}
	secondMTU.SetMTU(1320, true, false, links.MTUConstraint{Source: "second carrier update"})
	waitManagedAmnesiaLifecycle(t, func() bool {
		w.Lock()
		defer w.Unlock()
		return node.networkPayload.CapabilityRevision == secondMTU.Revision()
	}, "reattached MTU provider update was not processed")
	spawner.wait()
}

func TestManagedAmnesiaRecoveryRepublishesProfileTunConfig(t *testing.T) {
	profile := amnesiaProfileStatus{
		addresses:  []string{"10.205.10.1/32", "fd2b:12d:6d40:b125::1/128"},
		allowedIPs: []string{"0.0.0.0/0", "::/0"},
		dns:        []string{"10.205.20.53"},
		mtu:        1280,
	}
	tests := []struct {
		name       string
		withdrawal managedAmnesiaRuntimeResult
	}{
		{name: "activation error", withdrawal: managedAmnesiaRuntimeResult{err: errors.New("activate data plane")}},
		{name: "captcha", withdrawal: managedAmnesiaRuntimeResult{captcha: testAmnesiaCaptchaPrompt(t, 1, 1)}},
		{name: "inactive result", withdrawal: managedAmnesiaRuntimeResult{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spawner := &managedAmnesiaLifecycleSpawner{}
			runtime := managedAmnesiaLifecycleRuntime{
				kv: managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}, spawner: spawner,
			}
			class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
			w, err := pasta.WorkspaceFromConfig(
				[]pasta.NodeClass{class}, managedAmnesiaLifecycleConfig("amnesia recovery"),
				logging.NewDebugLogFactory(64),
			)
			if err != nil {
				t.Fatalf("build workspace: %v", err)
			}
			spawner.wait()
			t.Cleanup(func() {
				w.Close()
				spawner.wait()
			})
			node := class.onlyNode(t)
			peer := &managedAmnesiaLinkPeer{}
			peerID, err := w.AddNode(peer, "test/AmnesiaTunConfigPeer", "TUN config peer")
			if err != nil {
				t.Fatalf("add TUN peer: %v", err)
			}
			peerPort, err := w.AddPort(pasta.Port{
				Node: peerID, Direction: "right", Name: "Tun", Types: []string{nodekit.TypeTun},
			})
			if err != nil {
				t.Fatalf("add TUN peer port: %v", err)
			}
			if _, _, err := w.AddLink(peerPort, node.tunp); err != nil {
				t.Fatalf("attach TUN peer: %v", err)
			}
			spawner.wait()

			w.Lock()
			node.runtimeReady = true
			node.profileStatus = profile
			node.sendTunConfig()
			w.Unlock()
			waitManagedAmnesiaTunConfig(t, w, peer, func(config nodekit.TunConfig) bool {
				return managedAmnesiaTunConfigMatches(config, profile)
			}, "profile config")

			generation := node.generation.Load()
			withdrawal := tt.withdrawal
			withdrawal.generation = generation
			w.Lock()
			node.handleRuntimeResult(withdrawal)
			w.Unlock()
			waitManagedAmnesiaTunConfig(t, w, peer, nodekit.TunConfig.IsZero, "withdrawn config")

			w.Lock()
			node.handleRuntimeResult(managedAmnesiaRuntimeResult{
				generation: generation,
				ready:      true,
				profile:    profile,
			})
			w.Unlock()
			waitManagedAmnesiaTunConfig(t, w, peer, func(config nodekit.TunConfig) bool {
				return managedAmnesiaTunConfigMatches(config, profile)
			}, "recovered profile config")
		})
	}
}

func TestManagedAmnesiaRetriesTransientImportFailure(t *testing.T) {
	spawner := &managedAmnesiaLifecycleSpawner{}
	secrets := &amnesiaTestSecrets{values: map[string]string{
		"connection-secret": amnesiaTestActivationKey(t, "8f2c41d7a693b5e0"),
	}}
	runtime := managedAmnesiaLifecycleRuntime{
		kv: managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}, spawner: spawner,
		secrets: secrets,
	}
	recorder := &managedAmnesiaClientRecorder{
		t: t, peerKey: amnesiaTestPrivateKey(t).PublicKey(),
	}
	var attempts atomic.Uint64
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{
		Runtime: runtime,
		NewClient: func(options wgoamnesia.ClientOptions) (Client, error) {
			if attempts.Add(1) == 1 {
				return managedAmnesiaImportErrorClient{err: &net.DNSError{
					Err: "temporary fixture failure", Name: "gateway.test", IsNotFound: true,
				}}, nil
			}
			return recorder.factory(options)
		},
	}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class}, managedAmnesiaEnabledLifecycleConfig("amnesia"),
		logging.NewDebugLogFactory(128),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	t.Cleanup(func() {
		w.Close()
		spawner.wait()
	})
	node := class.onlyNode(t)

	tunProvider := &managedAmnesiaLinkPeer{}
	tunProviderID, err := w.AddNode(tunProvider, "test/AmnesiaTunProvider", "tun provider")
	if err != nil {
		t.Fatalf("add TUN provider: %v", err)
	}
	tunPort, err := w.AddPort(pasta.Port{
		Node: tunProviderID, Direction: "right", Name: "Tun", Types: []string{nodekit.TypeTun},
	})
	if err != nil {
		t.Fatalf("add TUN provider port: %v", err)
	}
	networkProvider := &managedAmnesiaLinkPeer{}
	networkProviderID, err := w.AddNode(networkProvider, "test/AmnesiaNetworkProvider", "network provider")
	if err != nil {
		t.Fatalf("add network provider: %v", err)
	}
	networkPort, err := w.AddPort(pasta.Port{
		Node: networkProviderID, Direction: "left", Name: "Network", Types: []string{nodekit.TypeNetwork},
	})
	if err != nil {
		t.Fatalf("add network provider port: %v", err)
	}
	tunLink, _, err := w.AddLink(tunPort, node.tunp)
	if err != nil {
		t.Fatalf("attach TUN link: %v", err)
	}
	networkLink, _, err := w.AddLink(networkPort, node.netp)
	if err != nil {
		t.Fatalf("attach network link: %v", err)
	}
	network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
	t.Cleanup(func() { _ = network.Close() })
	w.EmitEvent(networkProviderID, networkLink, nodekit.NetworkPayload{Network: network})
	tunSource, _ := newManagedAmnesiaLifecycleTun(t)
	w.EmitEvent(tunProviderID, tunLink, nodekit.TunPayload{Tun: tunSource})

	waitManagedAmnesiaNodeLabel(t, w, node.ID, "up")
	if got := attempts.Load(); got != 2 {
		t.Fatalf("Amnesia import attempts = %d, want 2", got)
	}
}

type managedAmnesiaImportErrorClient struct {
	err error
}

func (c managedAmnesiaImportErrorClient) ImportNonInteractive(
	context.Context,
	string,
	wgoamnesia.NegotiationOptions,
) (*wgoamnesia.Profile, error) {
	return nil, c.err
}

func TestManagedAmnesiaRecoveryReconfiguresVTunThroughJoiner(t *testing.T) {
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{
			tunnodes.TunJoinerClass{},
			tunnodes.VTunClass{},
		},
		configer.NewMemory(nil),
		logging.NewDebugLogFactory(64),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	t.Cleanup(w.Close)
	joinerID, err := w.AddNodeByClass(tunnodes.NodeTypeTunJoiner, "joiner")
	if err != nil {
		t.Fatalf("add TUN joiner: %v", err)
	}
	vtunID, err := w.AddNodeByClass(tunnodes.NodeTypeVTun, "vtun")
	if err != nil {
		t.Fatalf("add VTun: %v", err)
	}
	joiner, ok := w.NodeSnapshot(joinerID)
	if !ok {
		t.Fatal("TUN joiner snapshot is unavailable")
	}
	vtun, ok := w.NodeSnapshot(vtunID)
	if !ok {
		t.Fatal("VTun snapshot is unavailable")
	}
	configSource := &managedAmnesiaLinkPeer{}
	configSourceID, err := w.AddNode(configSource, "test/AmnesiaTunConfigSource", "Amnesia TUN config source")
	if err != nil {
		t.Fatalf("add TUN config source: %v", err)
	}
	configSourcePort, err := w.AddPort(pasta.Port{
		Node: configSourceID, Direction: "left", Name: "Tun", Types: []string{nodekit.TypeTun},
	})
	if err != nil {
		t.Fatalf("add TUN config source port: %v", err)
	}
	configLink, _, err := w.AddLink(configSourcePort, joiner.RightPorts[0])
	if err != nil {
		t.Fatalf("attach TUN config source to joiner: %v", err)
	}
	if _, _, err := w.AddLink(joiner.LeftPorts[1], vtun.RightPorts[0]); err != nil {
		t.Fatalf("attach VTun to TUN joiner secondary: %v", err)
	}
	receiver := &managedAmnesiaNetworkPeer{}
	receiverID, err := w.AddNode(receiver, "test/AmnesiaNetworkPeer", "network peer")
	if err != nil {
		t.Fatalf("add network peer: %v", err)
	}
	receiverPort, err := w.AddPort(pasta.Port{
		Node: receiverID, Direction: "right", Name: "Network", Types: []string{nodekit.TypeNetwork},
	})
	if err != nil {
		t.Fatalf("add network peer port: %v", err)
	}
	if _, _, err := w.AddLink(vtun.LeftPorts[0], receiverPort); err != nil {
		t.Fatalf("attach VTun network peer: %v", err)
	}

	profile := amnesiaProfileStatus{
		addresses:  []string{"10.205.10.1/32", "fd2b:12d:6d40:b125::1/128"},
		allowedIPs: []string{"0.0.0.0/0", "::/0"},
		dns:        []string{"10.205.20.53"},
		mtu:        1280,
	}
	w.EmitEvent(configSourceID, configLink, nodekit.TunConfig{
		Addrs:  slices.Clone(profile.addresses),
		Routes: slices.Clone(profile.allowedIPs),
		DNS:    firstAmnesiaDNS(profile.dns),
		MTU:    profile.mtu,
	})
	configured := waitManagedAmnesiaVTunRuntime(t, w, receiver, profile)

	w.EmitEvent(configSourceID, configLink, nodekit.TunConfig{})
	withdrawn := waitManagedAmnesiaVTunRuntime(t, w, receiver, amnesiaProfileStatus{
		dns: []string{tunnodes.DefaultVTunDNSServer},
		mtu: tunnodes.DefaultVTunMTU,
	})
	if nodekit.SameNetworkInstance(configured, withdrawn) {
		t.Fatal("TUN config withdrawal did not rebuild VTun")
	}
	if nodekit.NetworkIsReady(configured) {
		t.Fatal("TUN config withdrawal left the configured network ready")
	}

	w.EmitEvent(configSourceID, configLink, nodekit.TunConfig{
		Addrs:  slices.Clone(profile.addresses),
		Routes: slices.Clone(profile.allowedIPs),
		DNS:    firstAmnesiaDNS(profile.dns),
		MTU:    profile.mtu,
	})
	recovered := waitManagedAmnesiaVTunRuntime(t, w, receiver, profile)
	if nodekit.SameNetworkInstance(withdrawn, recovered) {
		t.Fatal("runtime recovery did not rebuild the withdrawn VTun")
	}
	if nodekit.NetworkIsReady(withdrawn) {
		t.Fatal("runtime recovery left the fallback network ready")
	}
}

func managedAmnesiaTunConfigMatches(got nodekit.TunConfig, want amnesiaProfileStatus) bool {
	return slices.Equal(got.Addrs, want.addresses) &&
		slices.Equal(got.Routes, want.allowedIPs) &&
		got.DNS == firstAmnesiaDNS(want.dns) && got.MTU == want.mtu
}

func waitManagedAmnesiaTunConfig(
	t *testing.T,
	w *pasta.Workspace,
	peer *managedAmnesiaLinkPeer,
	match func(nodekit.TunConfig) bool,
	description string,
) nodekit.TunConfig {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		w.Lock()
		var config nodekit.TunConfig
		if len(peer.configs) > 0 {
			config = peer.configs[len(peer.configs)-1].Clone()
		}
		w.Unlock()
		if match(config) {
			return config
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest TUN config = %#v, did not reach %s", config, description)
		}
		time.Sleep(time.Millisecond)
	}
}

func assertManagedAmnesiaVTunRuntime(
	t *testing.T,
	network nodekit.NetworkCloser,
	want amnesiaProfileStatus,
) {
	t.Helper()
	vtun, ok := nodekit.NetworkSource(network).(*netstackvtun.VTun)
	if !ok {
		t.Fatalf("network source = %T, want *vtun.VTun", nodekit.NetworkSource(network))
	}
	wantAddrs := make([]netip.Addr, 0, len(want.addresses))
	for _, prefix := range want.addresses {
		parsed, err := netip.ParsePrefix(prefix)
		if err != nil {
			t.Fatalf("parse wanted address %q: %v", prefix, err)
		}
		wantAddrs = append(wantAddrs, parsed.Addr())
	}
	wantDNS := make([]netip.Addr, 0, len(want.dns))
	for _, address := range want.dns {
		wantDNS = append(wantDNS, netip.MustParseAddr(address))
	}
	actualAddrs := slices.DeleteFunc(vtun.LocalAddrs(), func(address netip.Addr) bool {
		return address.IsLoopback()
	})
	actualMTU, mtuErr := vtun.MTU()
	if (want.addresses != nil && !slices.Equal(actualAddrs, wantAddrs)) ||
		!slices.Equal(vtun.GetDnsServers(), wantDNS) ||
		mtuErr != nil || actualMTU != want.mtu {
		t.Fatalf(
			"VTun runtime addresses=%v DNS=%v MTU=%d error=%v, want addresses=%v DNS=%v MTU=%d",
			actualAddrs, vtun.GetDnsServers(), actualMTU, mtuErr, wantAddrs, wantDNS, want.mtu,
		)
	}
}

func waitManagedAmnesiaVTunRuntime(
	t *testing.T,
	w *pasta.Workspace,
	receiver *managedAmnesiaNetworkPeer,
	want amnesiaProfileStatus,
) nodekit.NetworkCloser {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		w.Lock()
		var network nodekit.NetworkCloser
		if len(receiver.networks) > 0 {
			network = receiver.networks[len(receiver.networks)-1]
		}
		w.Unlock()
		if managedAmnesiaVTunRuntimeMatches(network, want) {
			return network
		}
		if time.Now().After(deadline) {
			assertManagedAmnesiaVTunRuntime(t, network, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func managedAmnesiaVTunRuntimeMatches(network nodekit.NetworkCloser, want amnesiaProfileStatus) bool {
	if !nodekit.NetworkIsReady(network) {
		return false
	}
	vtun, ok := nodekit.NetworkSource(network).(*netstackvtun.VTun)
	if !ok {
		return false
	}
	wantAddrs := make([]netip.Addr, 0, len(want.addresses))
	for _, prefix := range want.addresses {
		parsed, err := netip.ParsePrefix(prefix)
		if err != nil {
			return false
		}
		wantAddrs = append(wantAddrs, parsed.Addr())
	}
	wantDNS := make([]netip.Addr, 0, len(want.dns))
	for _, address := range want.dns {
		parsed, err := netip.ParseAddr(address)
		if err != nil {
			return false
		}
		wantDNS = append(wantDNS, parsed)
	}
	actualAddrs := slices.DeleteFunc(vtun.LocalAddrs(), func(address netip.Addr) bool {
		return address.IsLoopback()
	})
	actualMTU, err := vtun.MTU()
	return (want.addresses == nil || slices.Equal(actualAddrs, wantAddrs)) &&
		slices.Equal(vtun.GetDnsServers(), wantDNS) && err == nil && actualMTU == want.mtu
}

func TestManagedAmnesiaDNSLinkAndCountryChangeLifecycle(t *testing.T) {
	spawner := &managedAmnesiaLifecycleSpawner{}
	runtime := managedAmnesiaLifecycleRuntime{
		kv: managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}, spawner: spawner,
	}
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class}, managedAmnesiaLifecycleConfig("amnesia DNS"),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	spawner.wait()
	t.Cleanup(func() {
		w.Close()
		spawner.wait()
	})
	node := class.onlyNode(t)
	firstSource := newManagedAmnesiaTestDNS()
	replacementSource := newManagedAmnesiaTestDNS()
	t.Cleanup(func() {
		_ = firstSource.Close()
		_ = replacementSource.Close()
	})
	w.Lock()
	node.runtimeReady = true
	node.dnsProvider = firstSource
	w.Unlock()

	consumer := &managedAmnesiaDNSPeer{}
	consumerID, err := w.AddNode(consumer, "test/AmnesiaDNSConsumer", "DNS consumer")
	if err != nil {
		t.Fatalf("add DNS consumer: %v", err)
	}
	consumerPort, err := w.AddPort(pasta.Port{
		Node: consumerID, Direction: "right", Name: "DNS", Types: []string{dnsnodes.TypeDNS},
	})
	if err != nil {
		t.Fatalf("add DNS consumer port: %v", err)
	}
	firstLink, _, err := w.AddLink(node.dnsp, consumerPort)
	if err != nil {
		t.Fatalf("attach first DNS link: %v", err)
	}
	firstOutput := consumer.latest(t)
	if !dnsnodes.DNSIsReady(firstOutput) || !dnsnodes.SameDNSSource(firstOutput, firstSource) {
		t.Fatal("first DNS link did not receive a ready detached resolver")
	}

	generationBeforeRelink := node.generation.Load()
	w.RemoveLink(firstLink)
	if dnsnodes.DNSIsReady(firstOutput) {
		t.Fatal("removed DNS link left its detached resolver ready")
	}
	if !dnsnodes.DNSIsReady(firstSource) {
		t.Fatal("removing a DNS link closed the runtime resolver")
	}
	secondLink, _, err := w.AddLink(node.dnsp, consumerPort)
	if err != nil {
		t.Fatalf("reattach DNS link: %v", err)
	}
	secondOutput := consumer.latest(t)
	if secondLink == firstLink || dnsnodes.SameDNSInstance(firstOutput, secondOutput) ||
		!dnsnodes.SameDNSSource(secondOutput, firstSource) {
		t.Fatal("reattached DNS link did not receive a fresh detached resolver")
	}
	if node.generation.Load() != generationBeforeRelink {
		t.Fatal("DNS-only detach and reattach rebuilt the VPN runtime")
	}

	// Apply a country change through the node menu. The old output must close
	// before negotiation starts, so downstream nodes cannot use stale profile
	// DNS servers during the replacement.
	w.SendNodeFormularMsg(node.ID, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{
			Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node.ID),
		},
		BlockID: "config",
		Values:  map[string]any{fieldServerCountryCode: "de"},
	})
	if node.config.serverCountryCode != "de" {
		t.Fatalf("country after menu apply = %q, want de", node.config.serverCountryCode)
	}
	if dnsnodes.DNSIsReady(secondOutput) {
		t.Fatal("country change left the old detached resolver ready")
	}
	spawner.wait()

	// Model the ready result from the replacement runtime. The existing link
	// must receive a new wrapper without detach and reattach operations.
	w.Lock()
	node.handleRuntimeResult(managedAmnesiaRuntimeResult{
		generation: node.generation.Load(), ready: true, dns: replacementSource,
	})
	w.Unlock()
	replacementOutput := consumer.latest(t)
	if dnsnodes.SameDNSInstance(replacementOutput, secondOutput) ||
		!dnsnodes.DNSIsReady(replacementOutput) ||
		!dnsnodes.SameDNSSource(replacementOutput, replacementSource) {
		t.Fatal("ready country replacement did not refresh the attached DNS link")
	}
	if !dnsnodes.DNSIsReady(firstSource) {
		t.Fatal("country replacement output handling closed the old runtime source")
	}

	std.Request(w, consumerID, secondLink)
	refreshedOutput := consumer.latest(t)
	if dnsnodes.DNSIsReady(replacementOutput) ||
		dnsnodes.SameDNSInstance(refreshedOutput, replacementOutput) ||
		!dnsnodes.SameDNSSource(refreshedOutput, replacementSource) {
		t.Fatal("DNS value request did not replace the per-link wrapper")
	}
}

func TestManagedAmnesiaTerminalTunFailureRequestsReplacementAndKeepsRegistration(t *testing.T) {
	for _, test := range []struct {
		name       string
		dnsServers []string
	}{
		{name: "detached TUN"},
		{name: "tunnel DNS TUN", dnsServers: []string{"10.20.0.1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			spawner := &managedAmnesiaLifecycleSpawner{}
			secrets := &amnesiaTestSecrets{values: map[string]string{
				"connection-secret": amnesiaTestActivationKey(t, "8f2c41d7a693b5e0"),
			}}
			runtime := managedAmnesiaLifecycleRuntime{
				kv: managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}, spawner: spawner,
				secrets: secrets,
			}
			recorder := &managedAmnesiaClientRecorder{
				t: t, peerKey: amnesiaTestPrivateKey(t).PublicKey(),
				dnsByCountry: map[string][]string{"nl": test.dnsServers},
			}
			class := &recordingManagedAmnesiaClass{base: AmnesiaClass{
				Runtime: runtime, NewClient: recorder.factory,
			}}
			w, err := pasta.WorkspaceFromConfig(
				[]pasta.NodeClass{class}, managedAmnesiaEnabledLifecycleConfig("amnesia"),
				logging.NewDebugLogFactory(512),
			)
			if err != nil {
				t.Fatalf("build workspace: %v", err)
			}
			t.Cleanup(func() {
				w.Close()
				spawner.wait()
			})
			node := class.onlyNode(t)

			tunProvider := &managedAmnesiaLinkPeer{}
			tunProviderID, err := w.AddNode(tunProvider, "test/AmnesiaTunProvider", "tun provider")
			if err != nil {
				t.Fatalf("add TUN provider: %v", err)
			}
			tunPort, err := w.AddPort(pasta.Port{
				Node: tunProviderID, Direction: "right", Name: "Tun", Types: []string{nodekit.TypeTun},
			})
			if err != nil {
				t.Fatalf("add TUN provider port: %v", err)
			}
			networkProvider := &managedAmnesiaLinkPeer{}
			networkProviderID, err := w.AddNode(networkProvider, "test/AmnesiaNetworkProvider", "network provider")
			if err != nil {
				t.Fatalf("add network provider: %v", err)
			}
			networkPort, err := w.AddPort(pasta.Port{
				Node: networkProviderID, Direction: "left", Name: "Network", Types: []string{nodekit.TypeNetwork},
			})
			if err != nil {
				t.Fatalf("add network provider port: %v", err)
			}
			tunLink, _, err := w.AddLink(tunPort, node.tunp)
			if err != nil {
				t.Fatalf("attach TUN link: %v", err)
			}
			networkLink, _, err := w.AddLink(networkPort, node.netp)
			if err != nil {
				t.Fatalf("attach network link: %v", err)
			}
			network := gonnect.DetachNetwork(gonnect.NewLoopbackNetwork(), nil, nil)
			t.Cleanup(func() { _ = network.Close() })
			w.EmitEvent(networkProviderID, networkLink, nodekit.NetworkPayload{Network: network})

			firstTun, closeFirstTun := newManagedAmnesiaLifecycleTun(t)
			w.EmitEvent(tunProviderID, tunLink, nodekit.TunPayload{Tun: firstTun})
			waitManagedAmnesiaNodeLabel(t, w, node.ID, "up")
			firstObserver, firstTunGeneration, runtimeID, runtimeClient := managedAmnesiaRuntimeIdentity(t, node)
			if len(recorder.privateKeys) != 1 || firstObserver == nil || runtimeClient == nil {
				t.Fatalf(
					"initial runtime keys=%d observer=%p client=%v",
					len(recorder.privateKeys), firstObserver, runtimeClient,
				)
			}
			initialRequests := managedAmnesiaRequestCount(w, tunProvider)
			generationBeforeReconcile := node.generation.Load()

			// A reconcile can keep the same data-plane TUN. The observer must
			// report a later failure against the then-current node generation.
			w.Lock()
			node.scheduleReconcile(true)
			w.Unlock()
			waitManagedAmnesiaNodeLabel(t, w, node.ID, "up")
			reconciledObserver, reconciledTunGeneration, reconciledRuntimeID, reconciledClient :=
				managedAmnesiaRuntimeIdentity(t, node)
			if node.generation.Load() <= generationBeforeReconcile ||
				reconciledObserver != firstObserver || reconciledTunGeneration != firstTunGeneration ||
				reconciledRuntimeID != runtimeID || reconciledClient != runtimeClient {
				t.Fatal("same-TUN reconcile replaced or invalidated the active attachment identity")
			}

			closeFirstTun()
			waitManagedAmnesiaRequestAfterFailure(t, w, node, tunProvider, initialRequests)
			assertManagedAmnesiaRegistrationKept(t, node, runtimeID, runtimeClient)

			secondTun, closeSecondTun := newManagedAmnesiaLifecycleTun(t)
			w.EmitEvent(tunProviderID, tunLink, nodekit.TunPayload{Tun: secondTun})
			waitManagedAmnesiaNodeLabel(t, w, node.ID, "up")
			secondObserver, secondTunGeneration, secondRuntimeID, secondClient := managedAmnesiaRuntimeIdentity(t, node)
			if secondObserver == nil || secondObserver == firstObserver || secondRuntimeID != runtimeID ||
				secondClient != runtimeClient || len(recorder.privateKeys) != 1 {
				t.Fatalf(
					"first recovery observer=%p->%p runtime=%d->%d client_same=%t keys=%d",
					firstObserver, secondObserver, runtimeID, secondRuntimeID,
					secondClient == runtimeClient, len(recorder.privateKeys),
				)
			}

			// A delayed failure from the replaced observer must not invalidate the
			// live replacement or request another payload.
			requestsBeforeStale := managedAmnesiaRequestCount(w, tunProvider)
			currentGeneration := node.generation.Load()
			w.Lock()
			node.handleTunFailure(managedAmnesiaTunFailure{
				generation:    currentGeneration,
				tunGeneration: firstTunGeneration,
				runtimeID:     runtimeID,
				observer:      firstObserver,
				err:           errors.New("stale TUN attachment failure"),
			})
			node.handleTunFailure(managedAmnesiaTunFailure{
				generation:    currentGeneration,
				tunGeneration: secondTunGeneration,
				runtimeID:     runtimeID + 1,
				observer:      secondObserver,
				err:           errors.New("stale Amnesia runtime failure"),
			})
			w.Unlock()
			waitManagedAmnesiaNodeLabel(t, w, node.ID, "up")
			if got := managedAmnesiaRequestCount(w, tunProvider); got != requestsBeforeStale {
				t.Fatalf("stale failure request count = %d, want %d", got, requestsBeforeStale)
			}

			closeSecondTun()
			waitManagedAmnesiaRequestAfterFailure(t, w, node, tunProvider, requestsBeforeStale)
			assertManagedAmnesiaRegistrationKept(t, node, runtimeID, runtimeClient)
			thirdTun, _ := newManagedAmnesiaLifecycleTun(t)
			w.EmitEvent(tunProviderID, tunLink, nodekit.TunPayload{Tun: thirdTun})
			waitManagedAmnesiaNodeLabel(t, w, node.ID, "up")
			thirdObserver, _, thirdRuntimeID, thirdClient := managedAmnesiaRuntimeIdentity(t, node)
			if thirdObserver == nil || thirdObserver == secondObserver || thirdRuntimeID != runtimeID ||
				thirdClient != runtimeClient || len(recorder.privateKeys) != 1 {
				t.Fatalf(
					"second recovery observer=%p->%p runtime=%d->%d client_same=%t keys=%d",
					secondObserver, thirdObserver, runtimeID, thirdRuntimeID,
					thirdClient == runtimeClient, len(recorder.privateKeys),
				)
			}
		})
	}
}

func newManagedAmnesiaLifecycleTun(t *testing.T) (tun.Tun, func()) {
	t.Helper()
	source, peer := tun.Pipe(1, 1500, 0, 0)
	detached := tun.Detach(source, nil, nil)
	var once sync.Once
	closeTun := func() {
		once.Do(func() { _ = detached.Close() })
	}
	t.Cleanup(func() {
		closeTun()
		_ = source.Close()
		_ = peer.Close()
	})
	return detached, closeTun
}

func managedAmnesiaRuntimeIdentity(
	t *testing.T,
	node *managedAmnesiaNode,
) (*nodekit.TunFailureObserver, uint64, uint64, Client) {
	t.Helper()
	node.managedMu.Lock()
	defer node.managedMu.Unlock()
	return node.runtimeTunObserver, node.runtimeTunGeneration, node.runtimeID, node.runtimeClient
}

func assertManagedAmnesiaRegistrationKept(
	t *testing.T,
	node *managedAmnesiaNode,
	runtimeID uint64,
	client Client,
) {
	t.Helper()
	waitManagedAmnesiaLifecycle(t, func() bool {
		node.managedMu.Lock()
		defer node.managedMu.Unlock()
		return node.runtimeDevice == nil
	}, "failed Amnesia data plane was not closed")
	node.managedMu.Lock()
	defer node.managedMu.Unlock()
	if node.runtimeID != runtimeID || node.runtimeClient != client || node.runtimeConfig == nil {
		t.Fatalf(
			"TUN failure discarded registration: runtime=%d want=%d client_same=%t config=%p",
			node.runtimeID, runtimeID, node.runtimeClient == client, node.runtimeConfig,
		)
	}
}

func waitManagedAmnesiaRequestAfterFailure(
	t *testing.T,
	w *pasta.Workspace,
	node *managedAmnesiaNode,
	provider *managedAmnesiaLinkPeer,
	previous int,
) {
	t.Helper()
	waitManagedAmnesiaLifecycle(t, func() bool {
		snapshot, ok := w.NodeSnapshot(node.ID)
		return ok && snapshot.Label != "up" && managedAmnesiaRequestCount(w, provider) > previous
	}, "Amnesia did not leave up state and request a replacement TUN")
}

func waitManagedAmnesiaNodeLabel(t *testing.T, w *pasta.Workspace, node uint64, want string) {
	t.Helper()
	waitManagedAmnesiaLifecycle(t, func() bool {
		snapshot, ok := w.NodeSnapshot(node)
		return ok && snapshot.Label == want
	}, "Amnesia did not reach state "+want)
}

func managedAmnesiaRequestCount(w *pasta.Workspace, provider *managedAmnesiaLinkPeer) int {
	w.Lock()
	defer w.Unlock()
	return provider.requests
}

func TestManagedAmnesiaSaveRestorePreservesConfigAndInstallationUUID(t *testing.T) {
	manager := managedAmnesiaKVManager{store: newAmnesiaMemoryKV()}
	spawner := &managedAmnesiaLifecycleSpawner{}
	runtime := managedAmnesiaLifecycleRuntime{kv: manager, spawner: spawner}
	class := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	w, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{class},
		managedAmnesiaLifecycleConfig("saved amnesia"),
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("build workspace: %v", err)
	}
	spawner.wait()
	node := class.onlyNode(t)
	uuid, persistent, err := node.runtimeInstallationUUID(context.Background())
	if err != nil || !persistent || uuid == "" {
		t.Fatalf("initial installation UUID = %q persistent=%t err=%v", uuid, persistent, err)
	}

	saved := configer.NewMemory(nil)
	if err := w.SaveConfig(saved); err != nil {
		t.Fatalf("save workspace: %v", err)
	}
	w.Close()
	spawner.wait()

	restoredClass := &recordingManagedAmnesiaClass{base: AmnesiaClass{Runtime: runtime}}
	restoredWorkspace, err := pasta.WorkspaceFromConfig(
		[]pasta.NodeClass{restoredClass},
		saved,
		logging.NewDebugLogFactory(256),
	)
	if err != nil {
		t.Fatalf("restore workspace: %v", err)
	}
	spawner.wait()
	t.Cleanup(func() {
		restoredWorkspace.Close()
		spawner.wait()
	})
	restored := restoredClass.onlyNode(t)
	if restored.config != node.config {
		t.Fatalf("restored config = %#v, want %#v", restored.config, node.config)
	}
	restoredUUID, restoredPersistent, err := restored.runtimeInstallationUUID(context.Background())
	if err != nil || !restoredPersistent || restoredUUID != uuid {
		t.Fatalf(
			"restored installation UUID = %q persistent=%t err=%v, want %q",
			restoredUUID, restoredPersistent, err, uuid,
		)
	}
	snapshot, ok := restoredWorkspace.NodeSnapshot(restored.ID)
	if !ok || len(snapshot.LeftPorts) != 2 || len(snapshot.RightPorts) != 1 {
		t.Fatalf("restored Amnesia ports = %#v", snapshot)
	}
}

func managedAmnesiaLifecycleConfig(name string) configer.Config {
	return configer.NewMemory(map[string]any{
		name: map[string]any{
			"Class":                     NodeTypeAmnesia,
			fieldEnabled:                false,
			fieldConnectionSecret:       "connection-secret",
			fieldServerCountryCode:      "nl",
			nodekit.TLSConfigFieldID:    "default",
			fieldGatewayURL:             "https://gateway.example.test/",
			fieldGatewayPublicKeySecret: "gateway-key-secret",
		},
	})
}

func managedAmnesiaEnabledLifecycleConfig(name string) configer.Config {
	return configer.NewMemory(map[string]any{
		name: map[string]any{
			"Class":                NodeTypeAmnesia,
			fieldEnabled:           true,
			fieldConnectionSecret:  "connection-secret",
			fieldServerCountryCode: "nl",
		},
	})
}

type recordingManagedAmnesiaClass struct {
	base  AmnesiaClass
	nodes []*managedAmnesiaNode
}

func (c *recordingManagedAmnesiaClass) ClassName() string { return c.base.ClassName() }
func (c *recordingManagedAmnesiaClass) ShortDescription() string {
	return c.base.ShortDescription()
}
func (c *recordingManagedAmnesiaClass) LongDescription() string { return c.base.LongDescription() }
func (c *recordingManagedAmnesiaClass) DefaultNodeParams() pasta.NodeClassParams {
	return c.base.DefaultNodeParams()
}
func (c *recordingManagedAmnesiaClass) NewNode(
	cfg configer.Config,
	previous ...*pasta.NodeClassState,
) (pasta.Node, error) {
	raw, err := c.base.NewNode(cfg, previous...)
	if err != nil {
		return nil, err
	}
	node := raw.(*managedAmnesiaNode)
	c.nodes = append(c.nodes, node)
	return node, nil
}

func (c *recordingManagedAmnesiaClass) onlyNode(t *testing.T) *managedAmnesiaNode {
	t.Helper()
	if len(c.nodes) != 1 {
		t.Fatalf("constructed Amnesia nodes = %d, want 1", len(c.nodes))
	}
	return c.nodes[0]
}

type managedAmnesiaLinkPeer struct {
	pasta.BasicNode
	requests int
	configs  []nodekit.TunConfig
}

type managedAmnesiaNetworkPeer struct {
	pasta.BasicNode
	networks []nodekit.NetworkCloser
}

func (*managedAmnesiaNetworkPeer) PreLinkAdd(uint64, string, string) error { return nil }

func (p *managedAmnesiaNetworkPeer) OnEvent(
	event pasta.Event,
	_ string,
	_ []string,
	_ string,
) error {
	payload, ok := event.Payload.(nodekit.NetworkPayload)
	if ok && payload.Network != nil {
		p.networks = append(p.networks, payload.Network)
	}
	return nil
}

func (*managedAmnesiaLinkPeer) PreLinkAdd(uint64, string, string) error { return nil }

func (p *managedAmnesiaLinkPeer) OnEvent(
	event pasta.Event,
	_ string,
	_ []string,
	_ string,
) error {
	if std.IsRequest(event.Payload) {
		p.requests++
	}
	if config, ok := event.Payload.(nodekit.TunConfig); ok {
		p.configs = append(p.configs, config.Clone())
	}
	return nil
}

type managedAmnesiaDNSPeer struct {
	pasta.BasicNode

	received []dns.Interface
}

func (*managedAmnesiaDNSPeer) PreLinkAdd(uint64, string, string) error { return nil }

func (p *managedAmnesiaDNSPeer) OnEvent(
	event pasta.Event,
	linkType string,
	_ []string,
	receiverPortDirection string,
) error {
	if linkType != dnsnodes.TypeDNS || receiverPortDirection != "right" {
		return nil
	}
	payload, ok := event.Payload.(dnsnodes.DNSPayload)
	if ok && payload.DNS != nil {
		p.received = append(p.received, payload.DNS)
	}
	return nil
}

func (p *managedAmnesiaDNSPeer) latest(t *testing.T) dns.Interface {
	t.Helper()
	if len(p.received) == 0 {
		t.Fatal("DNS consumer received no resolver")
	}
	return p.received[len(p.received)-1]
}

type managedAmnesiaTestDNS struct {
	requests chan dns.Request
	closed   atomic.Bool
}

func newManagedAmnesiaTestDNS() *managedAmnesiaTestDNS {
	return &managedAmnesiaTestDNS{requests: make(chan dns.Request)}
}

func (d *managedAmnesiaTestDNS) Requests() chan<- dns.Request { return d.requests }
func (d *managedAmnesiaTestDNS) Close() error {
	d.closed.Store(true)
	return nil
}
func (d *managedAmnesiaTestDNS) IsUp() (bool, error) { return !d.closed.Load(), nil }

type managedAmnesiaKVManager struct{ store *amnesiaMemoryKV }

func (m managedAmnesiaKVManager) DefaultStore() storage.KVStore { return m.store }
func (m managedAmnesiaKVManager) PluginStore(name string) storage.KVStore {
	return m.store.Subtree("plugins", name)
}

type managedAmnesiaLifecycleRuntime struct {
	kv      storage.KVManager
	spawner nodekit.WorkerSpawner
	secrets storage.SecretsStorage
}

func (managedAmnesiaLifecycleRuntime) Context() context.Context { return context.Background() }
func (managedAmnesiaLifecycleRuntime) Pool() bufpool.Pool       { return nil }
func (managedAmnesiaLifecycleRuntime) System() sysnet.System    { return nil }
func (r managedAmnesiaLifecycleRuntime) KVManager() storage.KVManager {
	return r.kv
}
func (managedAmnesiaLifecycleRuntime) DnsCacheManager() storage.DnsCacheManager { return nil }
func (r managedAmnesiaLifecycleRuntime) SecretsStorage() storage.SecretsStorage { return r.secrets }
func (managedAmnesiaLifecycleRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (r managedAmnesiaLifecycleRuntime) WorkerSpawner(uint64) nodekit.WorkerSpawner {
	return r.spawner
}
func (managedAmnesiaLifecycleRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {
}

type managedAmnesiaLifecycleSpawner struct {
	next atomic.Uint64
	wg   sync.WaitGroup
}

func (s *managedAmnesiaLifecycleSpawner) Spawn(worker func(), _ string) (uint64, error) {
	id := s.next.Add(1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		worker()
	}()
	return id, nil
}

func (s *managedAmnesiaLifecycleSpawner) SpawnWg(
	worker func(),
	wg *sync.WaitGroup,
	name string,
) (uint64, error) {
	wg.Add(1)
	return s.Spawn(func() {
		defer wg.Done()
		worker()
	}, name)
}

func (s *managedAmnesiaLifecycleSpawner) wait() { s.wg.Wait() }

func waitManagedAmnesiaLifecycle(t *testing.T, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(failure)
		}
		time.Sleep(time.Millisecond)
	}
}
