// nolint
package almagest

import (
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/asciimoth/gonnect-netstack/vtun"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta/std"
)

const (
	defaultTunSpooferClientAddr = "10.113.213.2"
	defaultTunSpooferServerHost = "10.113.213.1"
	defaultTunSpooferResponse   = "Response from DefaultTun/Spoofer network"
)

type defaultTunSpooferE2EFixture struct {
	*httpE2E

	defaultTun uint64
	spoofer    uint64
	mock       uint64
	server     uint64
	response   uint64
	port       int
	client     *vtun.VTun
	p2p        *tun.Point2Point
	tunLink    uint64
}

func TestDefaultTunSpooferE2EHTTPThroughDebugSystem(t *testing.T) {
	f := setupDefaultTunSpooferE2E(t)
	f.assertDefaultTunSpooferRequestWorks()

	f.Engine.RemoveLink(E2EWorkspace, f.tunLink)
	f.WaitNodeLabel(f.spoofer, "waiting for tun")
	if _, err := f.defaultTunSpooferHTTPGet(); err == nil {
		t.Fatal("request after DefaultTun/Spoofer link removal succeeded, want failure")
	}

	f.tunLink = f.connectTunByPortName(f.spoofer, "left", "Tun", f.defaultTun, "right", "Tun")
	f.waitDefaultTunSpooferReady()
	f.assertDefaultTunSpooferRequestWorks()
}

func setupDefaultTunSpooferE2E(t *testing.T) defaultTunSpooferE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	f := defaultTunSpooferE2EFixture{httpE2E: e, port: httpTestPort()}
	f.defaultTun = e.AddNode(NodeTypeDefaultTun, "default-tun")
	f.spoofer = e.AddNode(NodeTypeSpoofer, "spoofer")
	f.mock = e.AddNode(NodeTypeMockNetwork, "mock")
	f.response = e.addStringConstant("server-response", defaultTunSpooferResponse)
	f.server = e.addHTTPServerOnHost("server", defaultTunSpooferServerHost, f.port)
	e.SubscribeMenus(f.defaultTun, f.spoofer, f.response, f.server)

	e.MustAddTypedLinkByPortName(std.TypeString, f.response, "right", "output", f.server, "left", "Response")
	f.tunLink = e.connectTunByPortName(f.spoofer, "left", "Tun", f.defaultTun, "right", "Tun")
	e.connectByPortName(f.mock, "left", "Network", f.spoofer, "right", "Network")
	e.connectByPortName(f.mock, "left", "Network", f.server, "right", "Network")

	f.waitDefaultTunSpooferReady()
	e.waitServerListening(f.server, fmt.Sprintf("%s:%d", defaultTunSpooferServerHost, f.port))
	f.attachDebugDefaultTunPeer()
	return f
}

func (f *defaultTunSpooferE2EFixture) attachDebugDefaultTunPeer() {
	f.T.Helper()
	entry := f.defaultTunPeer()
	if entry.Peer == nil {
		f.T.Fatal("debug DefaultTun peer is nil")
	}
	client, err := (&vtun.Opts{
		LocalAddrs:     []netip.Addr{netip.MustParseAddr(defaultTunSpooferClientAddr)},
		NoLoopbackAddr: true,
		Name:           "default-tun-client",
	}).Build()
	if err != nil {
		f.T.Fatalf("build VTun client: %v", err)
	}
	f.T.Cleanup(func() { _ = client.Close() })
	select {
	case <-client.Events():
	case <-time.After(2 * time.Second):
		f.T.Fatal("timed out waiting for VTun client to come up")
	}
	p2p := tun.NewP2P(nil, nil)
	p2p.SetA(client)
	p2p.SetB(entry.Peer)
	f.T.Cleanup(p2p.Stop)
	f.client = client
	f.p2p = p2p
}

func (f *defaultTunSpooferE2EFixture) defaultTunPeer() sysnetdebug.TunEntry {
	f.T.Helper()
	var entry sysnetdebug.TunEntry
	Eventually(f.T, func() bool {
		var ok bool
		entry, ok = f.DebugSystem.GetDefaultTunPeer()
		return ok && entry.Peer != nil
	}, "debug DefaultTun peer")
	return entry
}

func (f *defaultTunSpooferE2EFixture) waitDefaultTunSpooferReady() {
	f.T.Helper()
	f.WaitNodeLabel(f.defaultTun, "ready")
	f.WaitNodeLabel(f.spoofer, "forwarding")
}

func (f *defaultTunSpooferE2EFixture) assertDefaultTunSpooferRequestWorks() {
	f.T.Helper()
	Eventually(f.T, func() bool {
		body, err := f.defaultTunSpooferHTTPGet()
		if err != nil {
			return false
		}
		return body == defaultTunSpooferResponse
	}, "HTTP request through DefaultTun/Spoofer")
}

func (f *defaultTunSpooferE2EFixture) defaultTunSpooferHTTPGet() (string, error) {
	f.T.Helper()
	if f.client == nil {
		return "", fmt.Errorf("VTun client is not ready")
	}
	client := &http.Client{
		Transport: &http.Transport{DialContext: f.client.Dial},
		Timeout:   2 * time.Second,
	}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/", defaultTunSpooferServerHost, f.port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}
