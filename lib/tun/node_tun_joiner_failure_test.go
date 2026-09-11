package tunnodes

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunJoinerNodeRecoversFailedInputs(t *testing.T) {
	tests := []struct {
		name      string
		inputPort int
		wantLabel string
	}{
		{name: "default", inputPort: 0, wantLabel: "joined default"},
		{name: "secondary", inputPort: 1, wantLabel: "waiting for default"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
			t.Cleanup(func() { w.Close() })
			runtime := &tunSplitterTestRuntime{sys: &sysnetdebug.System{}}
			if err := w.AddNodeClass(TunJoinerClass{Runtime: runtime}); err != nil {
				t.Fatalf("AddNodeClass TunJoiner: %v", err)
			}

			providers := newTestTunProviderRegistry()
			failed := make(chan struct{})
			recoveryRead := make(chan struct{})
			trigger := make(chan struct{})
			if err := w.AddNodeClass(testTunProviderClass{
				registry: providers,
				wrapSource: func(source tun.Tun) tun.Tun {
					return &failFirstTunRead{
						Tun:                source,
						trigger:            trigger,
						failedSignal:       failed,
						recoveryReadSignal: recoveryRead,
					}
				},
			}); err != nil {
				t.Fatalf("AddNodeClass provider: %v", err)
			}

			joiner, err := w.AddNodeByClass(NodeTypeTunJoiner, "joiner")
			if err != nil {
				t.Fatalf("AddNodeByClass TunJoiner: %v", err)
			}
			provider, err := w.AddNodeByClass(testTunProviderNodeType, "provider")
			if err != nil {
				t.Fatalf("AddNodeByClass provider: %v", err)
			}
			receiver := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
			if _, err := w.AddNode(receiver, "test/TunReceiver", "receiver"); err != nil {
				t.Fatalf("AddNode receiver: %v", err)
			}

			joinerSnapshot := testkit.MustNodeSnapshot(t, w, joiner)
			providerSnapshot := testkit.MustNodeSnapshot(t, w, provider)
			if _, _, err := w.AddLink(joinerSnapshot.LeftPorts[test.inputPort], providerSnapshot.RightPorts[0]); err != nil {
				t.Fatalf("AddLink provider->joiner: %v", err)
			}
			if _, _, err := w.AddLink(receiver.in, joinerSnapshot.RightPorts[0]); err != nil {
				t.Fatalf("AddLink joiner->receiver: %v", err)
			}

			output := receiveTunPayload(t, receiver.events).Tun
			peer := providers.peer(t, provider)
			testkit.Eventually(t, func() bool {
				return testkit.MustNodeSnapshot(t, w, joiner).Label == test.wantLabel
			}, "joiner attaches its initial input")
			close(trigger)
			select {
			case <-failed:
			case <-time.After(time.Second):
				t.Fatal("injected input read failure did not occur")
			}
			select {
			case <-recoveryRead:
			case <-time.After(time.Second):
				t.Fatal("replacement input did not start reading")
			}
			testkit.Eventually(t, func() bool {
				return providers.sendCount(provider) >= 2 &&
					testkit.MustNodeSnapshot(t, w, joiner).Label == test.wantLabel
			}, "joiner recovers its failed input attachment")

			packet := testIPv4Packet([4]byte{10, 0, 0, 1}, [4]byte{10, 0, 0, 2})
			writeTunPacket(t, peer, packet)
			if got := readTunPacket(t, output); !bytes.Equal(got, packet) {
				t.Fatalf("joiner output packet = %x, want %x", got, packet)
			}
		})
	}
}

func TestTunJoinerNodeIgnoresStaleInputMessages(t *testing.T) {
	base, peer := tun.Pipe(1, 1500, 0, 0)
	t.Cleanup(func() {
		_ = base.Close()
		_ = peer.Close()
	})
	defaultInput := newTunInputFailureObserver(base, nil)
	secondaryInput := newTunInputFailureObserver(base, nil)

	tests := []struct {
		name string
		node *tunJoinerNode
		msg  tunJoinerInputFailed
	}{
		{
			name: "default generation",
			node: &tunJoinerNode{defaultLink: 7, defaultTun: defaultInput, defaultGen: 4},
			msg:  tunJoinerInputFailed{link: 7, generation: 3, role: tunJoinerInputDefault, err: errors.New("stale")},
		},
		{
			name: "default link",
			node: &tunJoinerNode{defaultLink: 8, defaultTun: defaultInput, defaultGen: 4},
			msg:  tunJoinerInputFailed{link: 7, generation: 4, role: tunJoinerInputDefault, err: errors.New("stale")},
		},
		{
			name: "secondary generation",
			node: &tunJoinerNode{
				secondaryTuns: map[uint64]tun.Tun{7: secondaryInput},
				secondaryGens: map[uint64]uint64{7: 4},
			},
			msg: tunJoinerInputFailed{link: 7, generation: 3, role: tunJoinerInputSecondary, err: errors.New("stale")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.node.handleInputFailed(test.msg); err != nil {
				t.Fatalf("handleInputFailed: %v", err)
			}
			switch test.msg.role {
			case tunJoinerInputDefault:
				if test.node.defaultTun != defaultInput || test.node.defaultGen != 4 {
					t.Fatal("stale failure changed the current default input")
				}
			case tunJoinerInputSecondary:
				if test.node.secondaryTuns[7] != secondaryInput || test.node.secondaryGens[7] != 4 {
					t.Fatal("stale failure changed the current secondary input")
				}
			}
		})
	}

	retryTests := []struct {
		name string
		node *tunJoinerNode
		msg  tunJoinerInputRetry
	}{
		{
			name: "default generation",
			node: &tunJoinerNode{defaultLink: 7, defaultGen: 4},
			msg:  tunJoinerInputRetry{link: 7, generation: 3, role: tunJoinerInputDefault},
		},
		{
			name: "default link",
			node: &tunJoinerNode{defaultLink: 8, defaultGen: 4},
			msg:  tunJoinerInputRetry{link: 7, generation: 4, role: tunJoinerInputDefault},
		},
		{
			name: "default already attached",
			node: &tunJoinerNode{defaultLink: 7, defaultTun: defaultInput, defaultGen: 4},
			msg:  tunJoinerInputRetry{link: 7, generation: 4, role: tunJoinerInputDefault},
		},
		{
			name: "secondary generation",
			node: &tunJoinerNode{
				secondaryTuns: map[uint64]tun.Tun{},
				secondaryGens: map[uint64]uint64{7: 4},
			},
			msg: tunJoinerInputRetry{link: 7, generation: 3, role: tunJoinerInputSecondary},
		},
		{
			name: "secondary already attached",
			node: &tunJoinerNode{
				secondaryTuns: map[uint64]tun.Tun{7: secondaryInput},
				secondaryGens: map[uint64]uint64{7: 4},
			},
			msg: tunJoinerInputRetry{link: 7, generation: 4, role: tunJoinerInputSecondary},
		},
	}
	for _, test := range retryTests {
		t.Run("retry/"+test.name, func(t *testing.T) {
			if err := test.node.handleInputRetry(test.msg); err != nil {
				t.Fatalf("handleInputRetry: %v", err)
			}
		})
	}
}
