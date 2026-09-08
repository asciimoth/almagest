package tunnodes

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

func TestTunJoinerForwardsTunConfigToInputsAndClearsOnOutputDisconnect(t *testing.T) {
	w := newTunJoinerConfigFlowWorkspace(t)
	defaultProvider := &tunConfigReceiverNode{configs: make(chan TunConfig, 8)}
	_, err := w.AddNode(defaultProvider, "test/TunConfigReceiver", "default")
	if err != nil {
		t.Fatalf("AddNode default provider: %v", err)
	}
	secondaryProvider := &tunConfigReceiverNode{configs: make(chan TunConfig, 8)}
	if _, err := w.AddNode(secondaryProvider, "test/TunConfigReceiver", "secondary"); err != nil {
		t.Fatalf("AddNode secondary provider: %v", err)
	}
	joiner, err := w.AddNodeByClass(NodeTypeTunJoiner, "joiner")
	if err != nil {
		t.Fatalf("AddNodeByClass TunJoiner: %v", err)
	}
	consumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	consumerID, err := w.AddNode(consumer, "test/TunReceiver", "consumer")
	if err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	joinerSnapshot := testkit.MustNodeSnapshot(t, w, joiner)
	if _, _, err := w.AddLink(joinerSnapshot.LeftPorts[0], defaultProvider.out); err != nil {
		t.Fatalf("AddLink default provider: %v", err)
	}
	if _, _, err := w.AddLink(joinerSnapshot.LeftPorts[1], secondaryProvider.out); err != nil {
		t.Fatalf("AddLink secondary provider: %v", err)
	}
	discardTunConfig(t, defaultProvider.configs)
	discardTunConfig(t, secondaryProvider.configs)

	outputLink, _, err := w.AddLink(consumer.in, joinerSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink output: %v", err)
	}
	cfg := TunConfig{Addrs: []string{"10.0.0.2/32"}, Routes: []string{"10.10.0.0/24"}, MTU: 1400, DNS: "8.8.8.8"}
	w.EmitEvent(consumerID, outputLink, cfg)
	assertTunConfigReceived(t, defaultProvider.configs, cfg)
	assertTunConfigReceived(t, secondaryProvider.configs, cfg)

	w.RemoveLink(outputLink)
	assertTunConfigReceived(t, defaultProvider.configs, TunConfig{})
	assertTunConfigReceived(t, secondaryProvider.configs, TunConfig{})
}

func TestDebugLogTunForwardsTunConfigAndClearsOnOutputDisconnect(t *testing.T) {
	logf := logging.NewDebugLogFactory(1024)
	w := pasta.NewWorkspace(logf)
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(DebugLogTunClass{}); err != nil {
		t.Fatalf("AddNodeClass DebugLogTun: %v", err)
	}
	provider := &tunConfigReceiverNode{configs: make(chan TunConfig, 8)}
	if _, err := w.AddNode(provider, "test/TunConfigReceiver", "provider"); err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	logger, err := w.AddNodeByClass(NodeTypeDebugLogTun, "tun log")
	if err != nil {
		t.Fatalf("AddNodeByClass DebugLogTun: %v", err)
	}
	consumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	consumerID, err := w.AddNode(consumer, "test/TunReceiver", "consumer")
	if err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	loggerSnapshot := testkit.MustNodeSnapshot(t, w, logger)
	if _, _, err := w.AddLink(loggerSnapshot.LeftPorts[0], provider.out); err != nil {
		t.Fatalf("AddLink input: %v", err)
	}
	discardTunConfig(t, provider.configs)
	outputLink, _, err := w.AddLink(consumer.in, loggerSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink output: %v", err)
	}

	cfg := TunConfig{Addrs: []string{"fd00::2/128"}, Routes: []string{"fd00:10::/64"}, MTU: 1280, DNS: "2001:4860:4860::8888"}
	w.EmitEvent(consumerID, outputLink, cfg)
	assertTunConfigReceived(t, provider.configs, cfg)
	assertDebugLogTunConfigLogged(t, logf, cfg)

	w.RemoveLink(outputLink)
	assertTunConfigReceived(t, provider.configs, TunConfig{})
}

func assertDebugLogTunConfigLogged(t *testing.T, logf *logging.DebugLogFactory, want TunConfig) {
	t.Helper()
	wantDNS := "dns=\"" + want.DNS + "\""
	for _, entry := range logf.Drain() {
		if entry.Source.NodeClass == NodeTypeDebugLogTun &&
			strings.Contains(entry.Message, "tun.TunConfig") &&
			strings.Contains(entry.Message, wantDNS) {
			return
		}
	}
	t.Fatalf("LogTun did not log TunConfig DNS %q", want.DNS)
}

func TestTunSplitterClearsConfigOnOutputDisconnectAndReplaysOnInputReconnect(t *testing.T) {
	w := newTunSplitterTestWorkspace(t)
	provider := &tunConfigReceiverNode{configs: make(chan TunConfig, 8)}
	if _, err := w.AddNode(provider, "test/TunConfigReceiver", "provider"); err != nil {
		t.Fatalf("AddNode provider: %v", err)
	}
	reconnectedProvider := &tunConfigReceiverNode{configs: make(chan TunConfig, 8)}
	if _, err := w.AddNode(reconnectedProvider, "test/TunConfigReceiver", "reconnected provider"); err != nil {
		t.Fatalf("AddNode reconnected provider: %v", err)
	}
	splitter, err := w.AddNodeByClass(NodeTypeTunSplitter, "splitter")
	if err != nil {
		t.Fatalf("AddNodeByClass TunSplitter: %v", err)
	}
	consumer := &tunPayloadReceiver{events: make(chan TunPayload, 2)}
	consumerID, err := w.AddNode(consumer, "test/TunReceiver", "consumer")
	if err != nil {
		t.Fatalf("AddNode consumer: %v", err)
	}

	splitterSnapshot := testkit.MustNodeSnapshot(t, w, splitter)
	inputLink, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], provider.out)
	if err != nil {
		t.Fatalf("AddLink input: %v", err)
	}
	discardTunConfig(t, provider.configs)
	outputLink, _, err := w.AddLink(consumer.in, splitterSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink output: %v", err)
	}

	cfg := TunConfig{Addrs: []string{"10.0.0.3/32"}, Routes: []string{"10.30.0.0/24"}, MTU: 1300, DNS: "9.9.9.9"}
	w.EmitEvent(consumerID, outputLink, cfg)
	assertTunConfigReceived(t, provider.configs, cfg.WithoutDNS().WithInferredSourceRoutes())

	w.RemoveLink(inputLink)
	reconnectedInputLink, _, err := w.AddLink(splitterSnapshot.LeftPorts[0], reconnectedProvider.out)
	if err != nil {
		t.Fatalf("AddLink reconnected input: %v", err)
	}
	_ = reconnectedInputLink
	assertTunConfigReceived(t, reconnectedProvider.configs, cfg.WithoutDNS().WithInferredSourceRoutes())

	w.RemoveLink(outputLink)
	assertTunConfigReceived(t, reconnectedProvider.configs, TunConfig{})
}

func TestDefaultTunForgetsTunConfigOnDisconnectAndReconnect(t *testing.T) {
	w, sys := newDefaultTunNodeTestWorkspace(t, false)
	node, err := w.AddNodeByClass(NodeTypeDefaultTun, "default tun")
	if err != nil {
		t.Fatalf("AddNodeByClass DefaultTun: %v", err)
	}
	receiver := &tunPayloadReceiver{events: make(chan TunPayload, 8)}
	receiverID, err := w.AddNode(receiver, "test/TunReceiver", "receiver")
	if err != nil {
		t.Fatalf("AddNode receiver: %v", err)
	}
	nodeSnapshot := testkit.MustNodeSnapshot(t, w, node)
	link, _, err := w.AddLink(receiver.in, nodeSnapshot.RightPorts[0])
	if err != nil {
		t.Fatalf("AddLink receiver: %v", err)
	}
	_ = receiveTunPayload(t, receiver.events)

	cfg := TunConfig{Addrs: []string{"10.70.0.2/32"}, Routes: []string{"10.71.0.0/16"}, MTU: 1300, DNS: "8.8.8.8"}
	w.EmitEvent(receiverID, link, cfg)
	_ = receiveTunPayload(t, receiver.events)
	wantSuggested := sysnet.DefaultTunOpts{
		TunAddrs:  []string{"10.70.0.2/32"},
		TunRoutes: []string{"10.71.0.0/16"},
		MTU:       1300,
		Exclude:   []sysnet.Rule{},
		Include:   []sysnet.Rule{},
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantSuggested) {
		t.Fatalf("opts with TunConfig = %#v, want %#v", got, wantSuggested)
	}

	w.RemoveLink(link)
	wantBase := sysnet.DefaultTunOpts{
		TunAddrs:  []string{},
		TunRoutes: []string{},
		Exclude:   []sysnet.Rule{},
		Include:   []sysnet.Rule{},
	}
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantBase) {
		t.Fatalf("opts after disconnect = %#v, want %#v", got, wantBase)
	}

	receiver2 := &tunPayloadReceiver{events: make(chan TunPayload, 4)}
	if _, err := w.AddNode(receiver2, "test/TunReceiver", "receiver 2"); err != nil {
		t.Fatalf("AddNode receiver2: %v", err)
	}
	if _, _, err := w.AddLink(receiver2.in, nodeSnapshot.RightPorts[0]); err != nil {
		t.Fatalf("AddLink receiver2: %v", err)
	}
	_ = receiveTunPayload(t, receiver2.events)
	if got := sys.lastOpts(); !reflect.DeepEqual(got, wantBase) {
		t.Fatalf("opts after reconnect = %#v, want %#v", got, wantBase)
	}
}

func newTunJoinerConfigFlowWorkspace(t *testing.T) *pasta.Workspace {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	if err := w.AddNodeClass(TunJoinerClass{Runtime: &tunSplitterTestRuntime{sys: &sysnetdebug.System{}}}); err != nil {
		t.Fatalf("AddNodeClass TunJoiner: %v", err)
	}
	return w
}

func newDefaultTunNodeTestWorkspace(t *testing.T, dynamic bool) (*pasta.Workspace, *recordingDefaultTunSystem) {
	t.Helper()
	w := pasta.NewWorkspace(logging.NewDebugLogFactory(1024))
	t.Cleanup(w.Close)
	sys := newRecordingDefaultTunSystem()
	sys.DisableDynDefaultTun = !dynamic
	if err := w.AddNodeClass(DefaultTunClass{Runtime: &tunSplitterTestRuntime{sys: sys}}); err != nil {
		t.Fatalf("AddNodeClass DefaultTun: %v", err)
	}
	return w, sys
}

type recordingDefaultTunSystem struct {
	*sysnetdebug.System

	buildOpts []sysnet.DefaultTunOpts
	tuns      []*recordingDefaultTun
}

func newRecordingDefaultTunSystem() *recordingDefaultTunSystem {
	return &recordingDefaultTunSystem{System: &sysnetdebug.System{}}
}

func (s *recordingDefaultTunSystem) BuildDefaultTun(opts sysnet.DefaultTunOpts) (sysnet.DefaultTun, error) {
	if err := s.VerifyDefaultTunOpts(opts); err != nil {
		return nil, err
	}
	dtun := newRecordingDefaultTun(opts.MTU)
	s.buildOpts = append(s.buildOpts, opts.Copy())
	s.tuns = append(s.tuns, dtun)
	return dtun, nil
}

func (s *recordingDefaultTunSystem) lastOpts() sysnet.DefaultTunOpts {
	if len(s.buildOpts) == 0 {
		return sysnet.DefaultTunOpts{}
	}
	return s.buildOpts[len(s.buildOpts)-1].Copy()
}

type recordingDefaultTun struct {
	tun.Tun
	peer tun.Tun

	mtu          int
	closeCounter atomic.Int32

	mu       sync.Mutex
	resolver dns.Interface
}

func newRecordingDefaultTun(mtu int) *recordingDefaultTun {
	if mtu <= 0 {
		mtu = 1500
	}
	base, peer := tun.Pipe(1, mtu, 0, 0)
	return &recordingDefaultTun{Tun: base, peer: peer, mtu: mtu}
}

func (t *recordingDefaultTun) SetDns(resolver dns.Interface) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.resolver = resolver
}

func (t *recordingDefaultTun) MTU() (int, error) {
	return t.mtu, nil
}

func (t *recordingDefaultTun) Close() error {
	t.closeCounter.Add(1)
	return errors.Join(t.Tun.Close(), t.peer.Close())
}
