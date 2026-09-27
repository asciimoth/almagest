package tunnodes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/tuntest"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunBuildsEmitsAndClosesPreviousBeforeRebuild(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, false)
	sys.RequirePreviousClosedBeforeBuild = true
	state, sub := testkit.SubscribeMenuState(t, w)
	defer w.UnsubscribeNotifications(sub)

	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}
	w.SubscribeNodeMenu(node, sub)
	testkit.AssertLeadingBlock(t, state, node, "effective")
	if got := sys.BuildCount(); got != 1 {
		t.Fatalf("build count after OnReady = %d, want 1", got)
	}

	receiver := tuntest.NewReceiver(8)
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.In, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	firstPayload := tuntest.ReceivePayload(t, receiver.Events)
	firstTun := sys.Tuns[0]
	w.EmitEvent(receiverID, link, TunConfig{DNS: "8.8.8.8"})
	if got := sys.BuildCount(); got != 1 {
		t.Fatalf("build count after DNS-only TunConfig = %d, want 1", got)
	}
	tuntest.AssertNoEvent(t, receiver.Events)

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "user-tun",
			"tunAddrs":  "10.60.0.2/32\nfd00::60/128",
			"tunRoutes": "10.70.0.0/16",
			"mtu":       1400,
		},
	})
	if got := sys.BuildCount(); got != 2 {
		t.Fatalf("build count after form apply = %d, want 2", got)
	}
	if firstTun.CloseCount() != 1 {
		t.Fatalf("old Tun close count during rebuild = %d, want 1", firstTun.CloseCount())
	}
	secondPayload := tuntest.ReceivePayload(t, receiver.Events)
	if nodekit.SameTunSource(firstPayload.Tun, secondPayload.Tun) {
		t.Fatal("form rebuild reused emitted Tun source, want a new Tun")
	}
	wantFormOpts := sysnet.TunOpts{
		Name:      "user-tun",
		TunAddrs:  []string{"10.60.0.2/32", "fd00::60/128"},
		TunRoutes: []string{"10.70.0.0/16"},
		MTU:       1400,
	}
	if got := sys.LastOpts(); !reflect.DeepEqual(got, wantFormOpts) {
		t.Fatalf("form build opts = %#v, want %#v", got, wantFormOpts)
	}
	if got := sys.SetNameCalls; len(got) != 0 {
		t.Fatalf("SetTunName calls = %#v, want creation-time name", got)
	}

	w.EmitEvent(receiverID, link, TunConfig{
		Addrs:  []string{"10.60.0.2/32", "10.80.0.2/32"},
		Routes: []string{"10.70.0.0/16", "10.81.0.0/16"},
		MTU:    1280,
		DNS:    "8.8.8.8",
	})
	if got := sys.BuildCount(); got != 3 {
		t.Fatalf("build count after TunConfig = %d, want 3", got)
	}
	_ = tuntest.ReceivePayload(t, receiver.Events)
	wantMergedOpts := wantFormOpts
	wantMergedOpts.TunAddrs = []string{"10.60.0.2/32", "fd00::60/128", "10.80.0.2/32"}
	wantMergedOpts.TunRoutes = []string{"10.70.0.0/16", "10.81.0.0/16"}
	wantMergedOpts.MTU = 1280
	if got := sys.LastOpts(); !reflect.DeepEqual(got, wantMergedOpts) {
		t.Fatalf("merged build opts = %#v, want %#v", got, wantMergedOpts)
	}
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "name", "user-tun")
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "tunAddrs", "10.60.0.2/32\nfd00::60/128\n10.80.0.2/32")
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "tunRoutes", "10.70.0.0/16\n10.81.0.0/16")
	testkit.AssertReadonlyMenuFieldValue(t, state, node, "effective", "mtu", 1280)
	if sys.BuildErr != nil {
		t.Fatalf("unexpected recording build error: %v", sys.BuildErr)
	}

	w.RemoveNode(node)
	testkit.Eventually(t, func() bool {
		return sys.Tuns[0].CloseCount() > 0 && sys.Tuns[1].CloseCount() > 0 && sys.Tuns[2].CloseCount() > 0
	}, "Tun instances to close")
}

func TestTunDynamicConfigUsesSettersWhenAvailable(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, true)
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}
	receiver := tuntest.NewReceiver(4)
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.In, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	_ = tuntest.ReceivePayload(t, receiver.Events)

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "dyn-tun",
			"tunAddrs":  "10.90.0.2/32",
			"tunRoutes": "10.91.0.0/16",
			"mtu":       1350,
		},
	})
	if got := sys.BuildCount(); got != 1 {
		t.Fatalf("build count after dynamic form apply = %d, want 1", got)
	}
	if got, want := sys.SetNameCalls, []string{"dyn-tun"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunName calls = %#v, want %#v", got, want)
	}
	if got, want := sys.SetAddrsCalls, [][]string{{"10.90.0.2/32"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunAddrs calls = %#v, want %#v", got, want)
	}
	if got, want := sys.SetRoutesCalls, [][]string{{"10.91.0.0/16"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunRoutes calls = %#v, want %#v", got, want)
	}
	if got, want := sys.SetMTUCalls, []int{1350}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunMTU calls = %#v, want %#v", got, want)
	}

	w.EmitEvent(receiverID, link, TunConfig{
		Addrs:  []string{"10.90.0.2/32", "10.92.0.2/32"},
		Routes: []string{"10.91.0.0/16", "10.93.0.0/16"},
		MTU:    1280,
		DNS:    "8.8.8.8",
	})
	if got := sys.BuildCount(); got != 1 {
		t.Fatalf("build count after dynamic TunConfig = %d, want 1", got)
	}
	if got, want := sys.SetAddrsCalls, [][]string{{"10.90.0.2/32"}, {"10.90.0.2/32", "10.92.0.2/32"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunAddrs calls after TunConfig = %#v, want %#v", got, want)
	}
	if got, want := sys.SetRoutesCalls, [][]string{{"10.91.0.0/16"}, {"10.91.0.0/16", "10.93.0.0/16"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunRoutes calls after TunConfig = %#v, want %#v", got, want)
	}
	if got, want := sys.SetMTUCalls, []int{1350, 1280}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SetTunMTU calls after TunConfig = %#v, want %#v", got, want)
	}
}

func TestTunNodeDynamicMTUGrowthKeepsTrafficOnSameSource(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, true)
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}
	receiver := tuntest.NewReceiver(4)
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.In, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink tun: %v", err)
	}
	output := tuntest.ReceivePayload(t, receiver.Events).Tun
	source := sys.Tuns[0]

	w.EmitEvent(receiverID, link, TunConfig{MTU: 9000})
	if got := sys.BuildCount(); got != 1 {
		t.Fatalf("build count after dynamic MTU growth = %d, want 1", got)
	}
	if got, err := output.MTU(); err != nil || got != 9000 {
		t.Fatalf("output MTU() = %d, %v; want 9000, nil", got, err)
	}

	trigger := bytes.Repeat([]byte{0x31}, 9000)
	trigger[0] = 0x45
	want := bytes.Repeat([]byte{0x42}, 9000)
	want[0] = 0x45
	result := readTunUntilPacket(output, want, 2)
	writeTunPacket(t, source.Peer, trigger)
	writeTunPacket(t, source.Peer, want)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("read after dynamic MTU growth: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for packet after dynamic MTU growth")
	}
	if sys.Tuns[0] != source {
		t.Fatal("dynamic MTU growth replaced the regular TUN source")
	}
}

func readTunUntilPacket(source tun.Tun, want []byte, maxReads int) <-chan error {
	result := make(chan error, 1)
	go func() {
		for range maxReads {
			offset := source.MRO()
			buf := make([]byte, offset+len(want))
			sizes := []int{0}
			count, err := source.Read([][]byte{buf}, sizes, offset)
			if err != nil {
				result <- err
				return
			}
			if count != 1 {
				result <- fmt.Errorf("read packet count %d, want 1", count)
				return
			}
			if sizes[0] == len(want) && bytes.Equal(buf[offset:offset+sizes[0]], want) {
				result <- nil
				return
			}
		}
		result <- errors.New("wanted packet did not arrive within read limit")
	}()
	return result
}

func TestTunNameValidationUsesSystem(t *testing.T) {
	w, sys := newTunNodeTestWorkspace(t, false)
	sys.TunNameChecker = func(name string) bool {
		return strings.HasPrefix(name, "ok")
	}
	node, err := w.AddNodeByClass(NodeTypeTun, "tun")
	if err != nil {
		t.Fatalf("AddNodeByClass Tun: %v", err)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "bad-name",
			"tunAddrs":  "",
			"tunRoutes": "",
			"mtu":       0,
		},
	})
	if got := sys.BuildCount(); got != 1 {
		t.Fatalf("build count after invalid name = %d, want unchanged 1", got)
	}
	snapshot := testkit.MustNodeSnapshot(t, w, node)
	if snapshot.Label != "invalid config" {
		t.Fatalf("node label after invalid name = %q, want invalid config", snapshot.Label)
	}

	w.SendNodeFormularMsg(node, formular.FormApplyMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageFormApply, MenuID: pasta.NodeMenuID(node)},
		BlockID:     "config",
		Values: map[string]any{
			"name":      "ok-name",
			"tunAddrs":  "",
			"tunRoutes": "",
			"mtu":       0,
		},
	})
	if got := sys.BuildCount(); got != 2 {
		t.Fatalf("build count after valid name = %d, want 2", got)
	}
	if got := sys.LastOpts().Name; got != "ok-name" {
		t.Fatalf("creation-time name = %q, want %q", got, "ok-name")
	}
}

func newTunNodeTestWorkspace(t *testing.T, dynamic bool) (*pasta.Workspace, *tuntest.RecordingSystem) {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	sys := tuntest.NewRecordingSystem()
	sys.DisableDynTun = !dynamic
	runtime := tunTestRuntime{ctx: context.Background(), sys: sys}
	if err := w.AddNodeClass(TunClass{Runtime: runtime}); err != nil {
		t.Fatalf("AddNodeClass Tun: %v", err)
	}
	return w, sys
}

type tunTestRuntime struct {
	ctx context.Context
	sys sysnet.System
}

func (r tunTestRuntime) Context() context.Context { return r.ctx }
func (tunTestRuntime) Pool() bufpool.Pool         { return nil }
func (r tunTestRuntime) System() sysnet.System    { return r.sys }
func (tunTestRuntime) KVManager() storage.KVManager {
	return nil
}
func (tunTestRuntime) DnsCacheManager() storage.DnsCacheManager {
	return nil
}
func (tunTestRuntime) SecretsStorage() storage.SecretsStorage {
	return nil
}
func (tunTestRuntime) TlsConfigStorage() storage.TlsConfigStorage {
	return nil
}
func (tunTestRuntime) WorkerSpawner(uint64) WorkerSpawner {
	return nil
}
func (tunTestRuntime) ObserveNodeSnapshot(uint64, pasta.NodeSnapshot) {}
