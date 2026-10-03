package tunnodes_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestDefaultTunRestoreMinimalPathTransfersAfterNativeDeviceReplacement(t *testing.T) {
	// This is the smallest failing restore sequence. The system replaces the
	// native device while it builds the downstream config. The stable public
	// wrapper exposes the new source after BuildDefaultTun returns, so the node
	// must use the source generation captured before the build.
	e, _, sinks, sys := newRestoredDefaultTunTopologyE2E(t, map[string]any{
		"00 default tun": map[string]any{
			"Class": NodeTypeDefaultTun,
			"Links": []any{"Tun -> [20 sink] Tun"},
		},
		"20 sink": map[string]any{
			"Class":             testTunSinkNodeType,
			"emitStartupConfig": true,
		},
	})

	sink := restoredNodeByName(t, e, "20 sink")
	waitForDefaultTunRebuild(t, sys)
	replaceDefaultTunNativeDevice(t, sinks, sys, sink)
	output := sinks.tun(t, sink)

	assertTunPacketTransferred(
		t,
		sys.lastPeer(),
		output,
		testIPv4Packet([4]byte{10, 90, 0, 1}, [4]byte{10, 210, 148, 1}),
	)
}

func TestDefaultTunRestoreComplexJoinedSplitPathTransfersAfterNativeDeviceReplacement(t *testing.T) {
	// This mirrors the relevant TUN spine in local/alm.hujson. It verifies that
	// the replacement payload reaches the final consumer through both routed
	// splitters, both joiners, the side branches, and the secondary providers.
	e, providers, sinks, sys := newRestoredDefaultTunTopologyE2E(t, restoredDefaultTunComplexNodes())

	entryJoiner := restoredNodeByName(t, e, "20 entry joiner")
	exitJoiner := restoredNodeByName(t, e, "40 exit joiner")
	entrySecondary := restoredNodeByName(t, e, "21 entry secondary")
	exitSecondary := restoredNodeByName(t, e, "41 exit secondary")
	hostSink := restoredNodeByName(t, e, "50 host sink")
	e.WaitNodeLabel(entryJoiner, "joined default + 1 secondary")
	e.WaitNodeLabel(exitJoiner, "joined default + 1 secondary")
	_ = providers.peer(t, entrySecondary)
	_ = providers.peer(t, exitSecondary)
	waitForDefaultTunRebuild(t, sys)
	replaceDefaultTunNativeDevice(t, sinks, sys, hostSink)
	output := sinks.tun(t, hostSink)

	assertTunPacketTransferred(
		t,
		sys.lastPeer(),
		output,
		testIPv4Packet([4]byte{10, 90, 0, 1}, [4]byte{10, 210, 148, 1}),
	)
}

func TestDefaultTunRestoreMinimalSplitPathRecoversAfterStartupReadFailure(t *testing.T) {
	failed := make(chan struct{})
	trigger := make(chan struct{})
	var triggerOnce sync.Once
	releaseFailure := func() { triggerOnce.Do(func() { close(trigger) }) }
	t.Cleanup(releaseFailure)
	e, _, sinks, sys := newRestoredDefaultTunTopologyE2E(t, map[string]any{
		"00 default tun": map[string]any{
			"Class": NodeTypeDefaultTun,
			"Links": []any{"Tun -> [10 entry splitter] Tun"},
		},
		"10 entry splitter": map[string]any{
			"Class": NodeTypeTunSplitter,
			"Links": []any{"slot 1 -> [20 host sink] Tun"},
			"rules": "TRUE\nSLOT 1",
		},
		"20 host sink": map[string]any{
			"Class":             testTunSinkNodeType,
			"emitStartupConfig": true,
		},
	}, func(source tun.Tun) tun.Tun {
		return &failFirstDefaultTunRead{Tun: source, trigger: trigger, failedSignal: failed}
	})

	waitForDefaultTunRebuild(t, sys)
	hostSink := restoredNodeByName(t, e, "20 host sink")
	initialPayloads := sinks.payloadCount(hostSink)
	releaseFailure()
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("injected DefaultTun read failure did not occur")
	}
	assertTunPacketTransferred(
		t,
		sys.lastPeer(),
		sinks.readyTunAfterPayloads(t, hostSink, initialPayloads+1),
		testIPv4Packet([4]byte{10, 90, 0, 1}, [4]byte{10, 210, 148, 1}),
	)
}

func TestDefaultTunRestoreComplexJoinedSplitPathRecoversAfterStartupReadFailure(t *testing.T) {
	failed := make(chan struct{})
	trigger := make(chan struct{})
	var triggerOnce sync.Once
	releaseFailure := func() { triggerOnce.Do(func() { close(trigger) }) }
	t.Cleanup(releaseFailure)
	e, providers, sinks, sys := newRestoredDefaultTunTopologyE2E(
		t,
		restoredDefaultTunComplexNodes(),
		func(source tun.Tun) tun.Tun {
			return &failFirstDefaultTunRead{Tun: source, trigger: trigger, failedSignal: failed}
		},
	)

	entryJoiner := restoredNodeByName(t, e, "20 entry joiner")
	exitJoiner := restoredNodeByName(t, e, "40 exit joiner")
	entrySecondary := restoredNodeByName(t, e, "21 entry secondary")
	exitSecondary := restoredNodeByName(t, e, "41 exit secondary")
	entryBranch := restoredNodeByName(t, e, "11 dns branch")
	hostSink := restoredNodeByName(t, e, "50 host sink")
	e.WaitNodeLabel(entryJoiner, "joined default + 1 secondary")
	e.WaitNodeLabel(exitJoiner, "joined default + 1 secondary")
	_ = providers.peer(t, entrySecondary)
	_ = providers.peer(t, exitSecondary)
	waitForDefaultTunRebuild(t, sys)
	initialEntryPayloads := sinks.payloadCount(entryBranch)
	initialHostPayloads := sinks.payloadCount(hostSink)
	releaseFailure()
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("injected DefaultTun read failure did not occur")
	}
	// The entry Splitter replaces its frontends after the input fails. The
	// downstream Joiners keep their own stable output objects, so the final
	// sink does not receive a replacement payload. Use the side branch to wait
	// for the entry replacement before testing the complete existing path.
	_ = sinks.readyTunAfterPayloads(t, entryBranch, initialEntryPayloads+1)

	assertTunPacketTransferred(
		t,
		sys.lastPeer(),
		sinks.readyTunAfterPayloads(t, hostSink, initialHostPayloads),
		testIPv4Packet([4]byte{10, 90, 0, 1}, [4]byte{10, 210, 148, 1}),
	)
}

func TestDefaultTunRestoreGeneratedGraphDepthsTransferAfterNativeDeviceReplacement(t *testing.T) {
	for _, depth := range []int{1, 4, 8} {
		t.Run(fmt.Sprintf("depth %d", depth), func(t *testing.T) {
			nodes, joinerNames := restoredDefaultTunGeneratedNodes(depth)
			e, providers, sinks, sys := newRestoredDefaultTunTopologyE2E(t, nodes)
			for _, name := range joinerNames {
				e.WaitNodeLabel(restoredNodeByName(t, e, name), "joined default + 1 secondary")
				_ = providers.peer(t, restoredNodeByName(t, e, name+" secondary"))
			}
			waitForDefaultTunRebuild(t, sys)
			sink := restoredNodeByName(t, e, "99 host sink")
			replaceDefaultTunNativeDevice(t, sinks, sys, sink)
			output := sinks.tun(t, sink)
			peer := sys.lastPeer()

			assertTunPacketTransferred(
				t,
				peer,
				output,
				testIPv4TransportPacket(
					[4]byte{10, 90, 0, 1},
					[4]byte{10, 210, 148, 1},
					testIPProtocolTCP,
					41001,
					443,
				),
			)
			assertTunPacketTransferred(
				t,
				peer,
				output,
				testIPv6TransportPacket(
					[16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
					[16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
					testIPProtocolUDP,
					41002,
					51820,
				),
			)
		})
	}
}

func TestDefaultTunRestoreDeepGraphSurvivesRepeatedSameSourceMTUChanges(t *testing.T) {
	nodes, joinerNames := restoredDefaultTunGeneratedNodes(8)
	e, providers, sinks, sys := newRestoredDefaultTunTopologyE2E(t, nodes)
	for _, name := range joinerNames {
		e.WaitNodeLabel(restoredNodeByName(t, e, name), "joined default + 1 secondary")
		_ = providers.peer(t, restoredNodeByName(t, e, name+" secondary"))
	}
	waitForDefaultTunRebuild(t, sys)
	sink := restoredNodeByName(t, e, "99 host sink")
	output := sinks.tun(t, sink)
	publicTun := sys.lastTun()
	initialGeneration := publicTun.SourceGeneration()

	previousBuilds := sys.buildCount()
	sinks.configure(t, sink, TunConfig{
		Addrs:  []string{"10.221.0.2/32"},
		Routes: []string{"0.0.0.0/0", "::/0"},
		MTU:    9000,
	})
	Eventually(t, func() bool {
		return sys.buildCount() >= previousBuilds+1
	}, "DefaultTun same-source jumbo-MTU rebuild")
	if sys.lastTun() != publicTun {
		t.Fatal("same-source rebuild replaced the stable public DefaultTun")
	}
	if got := publicTun.SourceGeneration(); got != initialGeneration {
		t.Fatalf("same-source generation = %d, want %d", got, initialGeneration)
	}

	trigger := testSizedIPv4UDPPacket(9000, 0x31)
	want := testSizedIPv6UDPPacket(9000, 0x42)
	result := readUntilTunPacketAsync(output, want, 2)
	writeTunPacket(t, publicTun.peer, trigger)
	writeTunPacket(t, publicTun.peer, want)
	assertMatchedTunPacketResult(t, result, "deep graph after jumbo MTU growth")

	for _, mtu := range []int{1280, 1420, 1428, 1500, 9000} {
		previousBuilds = sys.buildCount()
		sinks.configure(t, sink, TunConfig{
			Addrs:  []string{"10.221.0.2/32"},
			Routes: []string{"0.0.0.0/0", "::/0"},
			MTU:    mtu,
		})
		Eventually(t, func() bool {
			return sys.buildCount() >= previousBuilds+1
		}, fmt.Sprintf("DefaultTun rebuild at MTU %d", mtu))
		packet := testIPv4TransportPacket(
			[4]byte{10, 90, 0, 1},
			[4]byte{10, 210, 148, 1},
			testIPProtocolUDP,
			uint16(40000+mtu%1000),
			51820,
		)
		assertTunPacketTransferred(t, publicTun.peer, output, packet)
	}
}

func restoredDefaultTunGeneratedNodes(depth int) (map[string]any, []string) {
	nodes := map[string]any{}
	layerNames := make([]string, depth)
	layerClasses := make([]string, depth)
	joinerNames := make([]string, 0, depth/2)
	for i := range depth {
		if i%2 == 0 {
			layerNames[i] = fmt.Sprintf("%02d layer %02d splitter", 10+i, i+1)
			layerClasses[i] = NodeTypeTunSplitter
		} else {
			layerNames[i] = fmt.Sprintf("%02d layer %02d joiner", 10+i, i+1)
			layerClasses[i] = NodeTypeTunJoiner
			joinerNames = append(joinerNames, layerNames[i])
		}
	}
	inputPort := func(class string) string {
		if class == NodeTypeTunJoiner {
			return "Default"
		}
		return "Tun"
	}
	outputPort := func(class string) string {
		if class == NodeTypeTunSplitter {
			return "slot 1"
		}
		return "Tun"
	}

	nodes["00 default tun"] = map[string]any{
		"Class": NodeTypeDefaultTun,
		"Links": []any{fmt.Sprintf("Tun -> [%s] %s", layerNames[0], inputPort(layerClasses[0]))},
	}
	for i, name := range layerNames {
		nextName := "99 host sink"
		nextPort := "Tun"
		if i+1 < len(layerNames) {
			nextName = layerNames[i+1]
			nextPort = inputPort(layerClasses[i+1])
		}
		node := map[string]any{
			"Class": layerClasses[i],
			"Links": []any{fmt.Sprintf("%s -> [%s] %s", outputPort(layerClasses[i]), nextName, nextPort)},
		}
		if layerClasses[i] == NodeTypeTunSplitter {
			node["rules"] = "TRUE\nSLOT 1"
		} else {
			secondaryName := name + " secondary"
			nodes[secondaryName] = map[string]any{
				"Class": testTunProviderNodeType,
				"Links": []any{fmt.Sprintf("Tun -> [%s] Secondary", name)},
			}
		}
		nodes[name] = node
	}
	nodes["99 host sink"] = map[string]any{
		"Class":             testTunSinkNodeType,
		"emitStartupConfig": true,
	}
	return nodes, joinerNames
}

func testSizedIPv4UDPPacket(size int, fill byte) []byte {
	packet := testIPv4TransportPacket(
		[4]byte{10, 90, 0, 1},
		[4]byte{10, 210, 148, 1},
		testIPProtocolUDP,
		41001,
		51820,
	)
	packet = append(packet, make([]byte, size-len(packet))...)
	binary.BigEndian.PutUint16(packet[2:4], uint16(size))
	binary.BigEndian.PutUint16(packet[24:26], uint16(size-20))
	for i := 28; i < len(packet); i++ {
		packet[i] = fill
	}
	return packet
}

func testSizedIPv6UDPPacket(size int, fill byte) []byte {
	packet := testIPv6TransportPacket(
		[16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		[16]byte{0x20, 0x01, 0x0d, 0xb8, 0, 2, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
		testIPProtocolUDP,
		41002,
		51820,
	)
	packet = append(packet, make([]byte, size-len(packet))...)
	binary.BigEndian.PutUint16(packet[4:6], uint16(size-40))
	binary.BigEndian.PutUint16(packet[44:46], uint16(size-40))
	for i := 48; i < len(packet); i++ {
		packet[i] = fill
	}
	return packet
}

func restoredDefaultTunComplexNodes() map[string]any {
	return map[string]any{
		"00 default tun": map[string]any{
			"Class": NodeTypeDefaultTun,
			"Links": []any{"Tun -> [10 entry splitter] Tun"},
		},
		"10 entry splitter": map[string]any{
			"Class": NodeTypeTunSplitter,
			"Links": []any{
				"slot 1 -> [20 entry joiner] Default",
				"slot 2 -> [11 dns branch] Tun",
			},
			"rules": "TRUE\nSLOT 1",
		},
		"11 dns branch": map[string]any{
			"Class": testTunSinkNodeType,
		},
		"20 entry joiner": map[string]any{
			"Class": NodeTypeTunJoiner,
			"Links": []any{"Tun -> [30 route splitter] Tun"},
		},
		"21 entry secondary": map[string]any{
			"Class": testTunProviderNodeType,
			"Links": []any{"Tun -> [20 entry joiner] Secondary"},
		},
		"30 route splitter": map[string]any{
			"Class": NodeTypeTunSplitter,
			"Links": []any{
				"slot 1 -> [40 exit joiner] Default",
				"slot 2 -> [31 overlay branch] Tun",
			},
			"rules": "TRUE\nSLOT 1",
		},
		"31 overlay branch": map[string]any{
			"Class": testTunSinkNodeType,
		},
		"40 exit joiner": map[string]any{
			"Class": NodeTypeTunJoiner,
			"Links": []any{"Tun -> [50 host sink] Tun"},
		},
		"41 exit secondary": map[string]any{
			"Class": testTunProviderNodeType,
			"Links": []any{"Tun -> [40 exit joiner] Secondary"},
		},
		"50 host sink": map[string]any{
			"Class":             testTunSinkNodeType,
			"emitStartupConfig": true,
		},
	}
}

func newRestoredDefaultTunTopologyE2E(
	t *testing.T,
	nodes map[string]any,
	sourceWrappers ...func(tun.Tun) tun.Tun,
) (*NodeE2E, *testTunProviderRegistry, *testTunSinkRegistry, *recordingDefaultTunSystem) {
	t.Helper()
	pool := newE2ETestDebugPool(t)
	logf := NewDebugLogFactory(16384)
	sys := newRecordingDefaultTunSystem()
	sys.DisableDynDefaultTun = true
	nativeFile, err := os.CreateTemp(t.TempDir(), "restored-default-tun-device-")
	if err != nil {
		t.Fatalf("CreateTemp native device identity: %v", err)
	}
	t.Cleanup(func() { _ = nativeFile.Close() })
	replacementNativeFile, err := os.CreateTemp(t.TempDir(), "restored-default-tun-replacement-")
	if err != nil {
		t.Fatalf("CreateTemp replacement native device identity: %v", err)
	}
	t.Cleanup(func() { _ = replacementNativeFile.Close() })
	sys.nativeFile = nativeFile
	sys.stablePublic = true
	// Use mutable file identity from the first generation, but do not replace
	// the native device until the test has drained all restored-startup work.
	sys.replaceNativeOnBuild = 1 << 30
	sys.replacementNativeFile = replacementNativeFile
	sys.tunWrapper = func(source tun.Tun) tun.Tun {
		wrapped := tun.Tun(newRejectConcurrentReadTun(source))
		for _, wrap := range sourceWrappers {
			if wrap != nil {
				wrapped = wrap(wrapped)
			}
		}
		return wrapped
	}
	providers := newTestTunProviderRegistry()
	sinks := newTestTunSinkRegistry()
	engine, err := BuildEngine(
		context.Background(),
		configer.NewMemory(map[string]any{"nodes": nodes}),
		logf,
		sys,
		nil,
		nil,
		nil,
		nil,
		[]pasta.NodeClass{
			testTunProviderClass{registry: providers},
			testTunSinkClass{registry: sinks},
		},
		pool,
		true,
	)
	if err != nil {
		t.Fatalf("BuildEngine restore: %v", err)
	}
	t.Cleanup(func() {
		if err := engine.Close(); err != nil {
			t.Errorf("restored engine.Close: %v", err)
		}
	})
	return NewNodeE2EFromEngine(t, engine, logf, sys.System, nil), providers, sinks, sys
}

func replaceDefaultTunNativeDevice(
	t *testing.T,
	sinks *testTunSinkRegistry,
	sys *recordingDefaultTunSystem,
	sink uint64,
) {
	t.Helper()
	// Model downstream runtimes that withdraw their profile after a startup
	// error and later publish it again. These in-place rebuilds must settle
	// before the system replaces the native device on the next recovery.
	for range 2 {
		sinks.configure(t, sink, TunConfig{})
		sinks.configure(t, sink, TunConfig{
			Addrs:  []string{"10.221.0.2/32"},
			Routes: []string{"0.0.0.0/0"},
			MTU:    1300,
		})
	}
	waitForDefaultTunBuildsStable(t, sys)
	previousBuilds := sys.buildCount()
	wantBuild := sys.scheduleNativeReplacement()
	if wantBuild != previousBuilds+1 {
		t.Fatalf("replacement build = %d, want %d", wantBuild, previousBuilds+1)
	}
	sinks.configure(t, sink, TunConfig{
		Addrs:  []string{"10.221.0.2/32"},
		Routes: []string{"0.0.0.0/0", "10.222.0.0/16"},
		MTU:    1300,
	})
	Eventually(t, func() bool {
		return sys.buildCount() >= wantBuild
	}, "DefaultTun native device replacement")
}

func waitForDefaultTunBuildsStable(t *testing.T, sys *recordingDefaultTunSystem) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	stable := time.NewTimer(100 * time.Millisecond)
	defer stable.Stop()
	count := sys.buildCount()
	for {
		select {
		case <-stable.C:
			if sys.buildCount() == count {
				return
			}
			count = sys.buildCount()
			stable.Reset(100 * time.Millisecond)
		case <-deadline.C:
			t.Fatal("DefaultTun builds did not become stable")
		}
	}
}

type rejectConcurrentReadTun struct {
	tun.Tun

	mu      sync.Mutex
	reading bool
}

type failFirstDefaultTunRead struct {
	tun.Tun

	mu           sync.Mutex
	failed       bool
	trigger      <-chan struct{}
	failedSignal chan struct{}
}

func (t *failFirstDefaultTunRead) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.mu.Lock()
	if !t.failed {
		t.failed = true
		t.mu.Unlock()
		if t.trigger != nil {
			<-t.trigger
		}
		if t.failedSignal != nil {
			close(t.failedSignal)
		}
		return 0, errors.New("injected DefaultTun startup read failure")
	}
	t.mu.Unlock()
	return t.Tun.Read(bufs, sizes, offset)
}

func newRejectConcurrentReadTun(source tun.Tun) *rejectConcurrentReadTun {
	return &rejectConcurrentReadTun{Tun: source}
}

func (t *rejectConcurrentReadTun) Read(bufs [][]byte, sizes []int, offset int) (int, error) {
	t.mu.Lock()
	if t.reading {
		t.mu.Unlock()
		return 0, errors.New("concurrent native TUN read")
	}
	t.reading = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.reading = false
		t.mu.Unlock()
	}()
	return t.Tun.Read(bufs, sizes, offset)
}

func restoredNodeByName(t *testing.T, e *NodeE2E, name string) uint64 {
	t.Helper()
	id, ok := e.Engine.NodeByName(E2EWorkspace, name)
	if !ok {
		t.Fatalf("restored node %q missing", name)
	}
	return id
}

func waitForDefaultTunRebuild(t *testing.T, sys *recordingDefaultTunSystem) {
	t.Helper()
	Eventually(t, func() bool {
		return sys.buildCount() >= 2 && sys.lastPeer() != nil
	}, "restored DefaultTun rebuild")
}
