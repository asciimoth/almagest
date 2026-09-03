package tuntest

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
)

// Receiver is a test node that receives almagest/tun payloads.
type Receiver struct {
	pasta.BasicNode
	In     uint64
	Events chan links.TunPayload
}

// NewReceiver returns a TUN receiver test node.
func NewReceiver(size int) *Receiver {
	return &Receiver{Events: make(chan links.TunPayload, size)}
}

func (n *Receiver) OnInit(w *pasta.Workspace, _ pasta.Logger, id uint64, _ string, _ *pasta.NodeInitData, _, _, _, _ bool) error {
	port, err := w.AddPortLocked(pasta.Port{
		Node:      id,
		Direction: "left",
		Name:      "Tun",
		Types:     []string{links.TypeTun},
	})
	if err != nil {
		return err
	}
	n.In = port
	return nil
}

func (n *Receiver) PreLinkAdd(port uint64, linkType, portDirection string) error {
	if port != n.In || portDirection != "left" || linkType != links.TypeTun {
		return pasta.LinkTypeErr(linkType)
	}
	return links.TunPreLinkAdd(nil, 0, linkType)
}

func (n *Receiver) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort != n.In || receiverPortDirection != "left" || linkType != links.TypeTun {
		return nil
	}
	payload, ok := event.Payload.(links.TunPayload)
	if ok {
		n.Events <- payload
	}
	return nil
}

// ReceivePayload returns one TUN payload or fails the test.
func ReceivePayload(t *testing.T, events <-chan links.TunPayload) links.TunPayload {
	t.Helper()
	select {
	case payload := <-events:
		if payload.Tun == nil {
			t.Fatal("payload.Tun = nil, want tun")
		}
		return payload
	default:
		t.Fatal("no tun payload received")
		return links.TunPayload{}
	}
}

// AssertNoEvent fails if a TUN payload is ready.
func AssertNoEvent(t *testing.T, events <-chan links.TunPayload) {
	t.Helper()
	select {
	case payload := <-events:
		t.Fatalf("unexpected TUN payload: %#v", payload)
	default:
	}
}

// RecordingSystem records regular TUN builds and dynamic configuration calls.
type RecordingSystem struct {
	*sysnetdebug.System

	BuildOpts                        []sysnet.TunOpts
	Tuns                             []*RecordingTun
	SetMTUCalls                      []int
	SetAddrsCalls                    [][]string
	SetRoutesCalls                   [][]string
	SetNameCalls                     []string
	BuildErr                         error
	RequirePreviousClosedBeforeBuild bool
}

// NewRecordingSystem returns a sysnet system that records regular TUN changes.
func NewRecordingSystem() *RecordingSystem {
	return &RecordingSystem{System: &sysnetdebug.System{}}
}

func (s *RecordingSystem) BuildTun(opts sysnet.TunOpts) (tun.Tun, error) {
	if err := s.VerifyTunOpts(opts); err != nil {
		return nil, err
	}
	if s.RequirePreviousClosedBeforeBuild && len(s.Tuns) > 0 && s.Tuns[len(s.Tuns)-1].CloseCount() == 0 {
		s.BuildErr = errors.New("previous Tun was still open before BuildTun")
		return nil, s.BuildErr
	}
	if s.BuildErr != nil {
		return nil, s.BuildErr
	}
	rtun := NewRecordingTun(opts.MTU)
	s.BuildOpts = append(s.BuildOpts, CopyTunOpts(opts))
	s.Tuns = append(s.Tuns, rtun)
	return rtun, nil
}

func (s *RecordingSystem) SetTunMTU(t tun.Tun, mtu int) error {
	rtun, ok := t.(*RecordingTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.SetMTUCalls = append(s.SetMTUCalls, mtu)
	rtun.MTUValue = mtu
	return nil
}

func (s *RecordingSystem) SetTunAddrs(t tun.Tun, addrs []string) error {
	rtun, ok := t.(*RecordingTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.SetAddrsCalls = append(s.SetAddrsCalls, Clone(addrs))
	rtun.Addrs = Clone(addrs)
	return nil
}

func (s *RecordingSystem) SetTunRoutes(t tun.Tun, routes []string) error {
	rtun, ok := t.(*RecordingTun)
	if !ok {
		return sysnet.ErrUnknownTun
	}
	s.SetRoutesCalls = append(s.SetRoutesCalls, Clone(routes))
	rtun.Routes = Clone(routes)
	return nil
}

func (s *RecordingSystem) SetTunName(t tun.Tun, name string) ([]string, error) {
	rtun, ok := t.(*RecordingTun)
	if !ok {
		return nil, sysnet.ErrUnknownTun
	}
	if valid, free := s.TunNameVerify(name); !valid || (!free && name != rtun.NameValue) {
		return nil, sysnet.ErrUnknownTun
	}
	s.SetNameCalls = append(s.SetNameCalls, name)
	rtun.NameValue = name
	return []string{name}, nil
}

// BuildCount returns the count of BuildTun calls.
func (s *RecordingSystem) BuildCount() int {
	return len(s.BuildOpts)
}

// LastOpts returns the last recorded TUN build options.
func (s *RecordingSystem) LastOpts() sysnet.TunOpts {
	if len(s.BuildOpts) == 0 {
		return sysnet.TunOpts{}
	}
	return CopyTunOpts(s.BuildOpts[len(s.BuildOpts)-1])
}

// RecordingTun is a pipe TUN that records close and dynamic configuration state.
type RecordingTun struct {
	tun.Tun
	Peer tun.Tun

	NameValue    string
	MTUValue     int
	Addrs        []string
	Routes       []string
	closeCounter atomic.Int32
}

// NewRecordingTun returns a recording TUN.
func NewRecordingTun(mtu int) *RecordingTun {
	if mtu <= 0 {
		mtu = 1500
	}
	base, peer := tun.Pipe(1, mtu, 0, 0)
	return &RecordingTun{Tun: base, Peer: peer, MTUValue: mtu}
}

func (t *RecordingTun) MTU() (int, error) {
	return t.MTUValue, nil
}

func (t *RecordingTun) Close() error {
	t.closeCounter.Add(1)
	return errors.Join(t.Tun.Close(), t.Peer.Close())
}

// CloseCount returns the count of Close calls.
func (t *RecordingTun) CloseCount() int {
	return int(t.closeCounter.Load())
}

// CopyTunOpts returns a deep copy of regular TUN options.
func CopyTunOpts(opts sysnet.TunOpts) sysnet.TunOpts {
	return sysnet.TunOpts{
		TunAddrs:  Clone(opts.TunAddrs),
		TunRoutes: Clone(opts.TunRoutes),
		MTU:       opts.MTU,
	}
}

// Clone returns a copy of in.
func Clone[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}
