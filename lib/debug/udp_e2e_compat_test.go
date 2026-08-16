package debugnodes_test

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
	debugnodes "github.com/asciimoth/almagest/lib/debug"
	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/logging"
	networknodes "github.com/asciimoth/almagest/lib/network"
	socksnodes "github.com/asciimoth/almagest/lib/socks"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/almagest/lib/testkit"
	"github.com/asciimoth/almagest/lib/testkit/e2etest"
	tunnodes "github.com/asciimoth/almagest/lib/tun"
	wireguardnodes "github.com/asciimoth/almagest/lib/wireguard"
	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
	sysnetdebug "github.com/asciimoth/gonnect/sysnet/debug"
	"github.com/asciimoth/pasta/pasta"
	wgdevice "github.com/asciimoth/wgo/device"
)

const (
	E2EWorkspace = e2etest.E2EWorkspace
	TypeNetwork  = links.TypeNetwork
	TypeTun      = links.TypeTun

	NodeTypeUDPPingClient = debugnodes.NodeTypeUDPPingClient
	NodeTypeUDPPingServer = debugnodes.NodeTypeUDPPingServer

	NodeTypeMockNetwork = networknodes.NodeTypeMockNetwork
	NodeTypeOutNetwork  = networknodes.NodeTypeOutNetwork

	NodeTypeSocksClient = socksnodes.NodeTypeSocksClient
	NodeTypeSocksServer = socksnodes.NodeTypeSocksServer

	NodeTypeVTun      = tunnodes.NodeTypeVTun
	NodeTypeWireGuard = wireguardnodes.NodeTypeWireGuard

	wireGuardE2EClientAddr = "10.198.10.1"
	wireGuardE2EServerAddr = "10.198.20.1"
)

type (
	Engine         = almagest.Engine
	NodeE2E        = e2etest.NodeE2E
	Secret         = storage.Secret
	SecretsStorage = storage.SecretsStorage
)

func BuildEngine(ctx context.Context, cfg configer.Config, logf logging.LogFactory, sys sysnet.System, kv storage.KVManager, dns storage.DnsCacheManager, secrets storage.SecretsStorage, tlsConfigs storage.TlsConfigStorage, extra []pasta.NodeClass, pool bufpool.Pool, debug bool) (almagest.Engine, error) {
	return almagest.BuildEngine(ctx, cfg, logf, sys, kv, dns, secrets, tlsConfigs, extra, pool, debug)
}

func NewDebugLogFactory(buffer int) *logging.DebugLogFactory {
	return logging.NewDebugLogFactory(buffer)
}

func NewNodeE2E(t *testing.T) *NodeE2E {
	return e2etest.NewNodeE2E(t)
}

func NewNodeE2EFromEngine(t *testing.T, engine almagest.Engine, logf *logging.DebugLogFactory, sys *sysnetdebug.System, secrets *e2etest.SecretsStorage) *NodeE2E {
	return e2etest.NewNodeE2EFromEngine(t, engine, logf, sys, secrets)
}

func newE2EDebugSystem() *sysnetdebug.System {
	return e2etest.NewDebugSystem()
}

func newE2ETestDebugPool(t *testing.T) *e2etest.DebugPool {
	return e2etest.NewTestDebugPool(t)
}

func FormMessageBase(messageType string, node uint64) formular.MessageBase {
	return testkit.FormMessageBase(messageType, node)
}

func Eventually(t *testing.T, condition func() bool, format string, args ...any) {
	testkit.Eventually(t, condition, format, args...)
}

func FindItem(block formular.Block, id string) (formular.Item, bool) {
	return testkit.FindItem(block, id)
}

func (e *httpE2E) connectTunByPortName(leftNode uint64, leftDir, leftName string, rightNode uint64, rightDir, rightName string) uint64 {
	return e.MustAddTypedLinkByPortName(TypeTun, leftNode, leftDir, leftName, rightNode, rightDir, rightName)
}

func (e *httpE2E) applyTunCopyVTunConfig(node uint64, name, localAddrs string) {
	e.T.Helper()
	e.Engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"name":           name,
			"localAddrs":     localAddrs,
			"dnsServers":     "",
			"mtu":            1500,
			"endpointCh":     1024,
			"eventCh":        10,
			"noLoopbackAddr": false,
			"disableSACK":    false,
		},
	})
}

func (e *httpE2E) waitWireGuardUp(node uint64) {
	e.T.Helper()
	Eventually(e.T, func() bool {
		snapshot, ok := e.Engine.NodeSnapshot(E2EWorkspace, node)
		return ok && snapshot.Label == "up"
	}, "wireguard node %d up", node)
}

type wireGuardE2ENames struct {
	mock       string
	clientWG   string
	serverWG   string
	clientVTun string
	serverVTun string
}

func defaultWireGuardE2ENames(prefix string) wireGuardE2ENames {
	return wireGuardE2ENames{
		mock:       prefix + "mock",
		clientWG:   prefix + "client-wg",
		serverWG:   prefix + "server-wg",
		clientVTun: prefix + "client-vtun",
		serverVTun: prefix + "server-vtun",
	}
}

type wireGuardE2EFixture struct {
	*httpE2E

	mock       uint64
	clientWG   uint64
	serverWG   uint64
	clientVTun uint64
	serverVTun uint64
}

func setupWireGuardE2E(t *testing.T, names wireGuardE2ENames) wireGuardE2EFixture {
	t.Helper()
	e := newHTTPE2E(t)
	clientPrivate, clientPublic := wireGuardTestKeyPair(t, 41)
	serverPrivate, serverPublic := wireGuardTestKeyPair(t, 73)
	clientWGPort := httpTestPort()
	serverWGPort := httpTestPort()

	f := wireGuardE2EFixture{httpE2E: e}
	f.mock = e.AddNode(NodeTypeMockNetwork, names.mock)
	f.clientWG = e.AddNode(NodeTypeWireGuard, names.clientWG)
	f.serverWG = e.AddNode(NodeTypeWireGuard, names.serverWG)
	f.clientVTun = e.AddNode(NodeTypeVTun, names.clientVTun)
	f.serverVTun = e.AddNode(NodeTypeVTun, names.serverVTun)
	e.SubscribeMenus(f.clientWG, f.serverWG, f.clientVTun, f.serverVTun)

	e.applyTunCopyVTunConfig(f.clientVTun, "wireguard-client-vtun", wireGuardE2EClientAddr+"/24")
	e.applyTunCopyVTunConfig(f.serverVTun, "wireguard-server-vtun", wireGuardE2EServerAddr+"/24")
	applyWireGuardConfig(t, e.Engine, f.clientWG, clientPrivate, clientWGPort, []wireGuardPeerConfig{{
		id:                          "server",
		publicKey:                   serverPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", serverWGPort),
		allowedIPs:                  wireGuardE2EServerAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})
	applyWireGuardConfig(t, e.Engine, f.serverWG, serverPrivate, serverWGPort, []wireGuardPeerConfig{{
		id:                          "client",
		publicKey:                   clientPublic,
		endpoint:                    fmt.Sprintf("127.0.0.1:%d", clientWGPort),
		allowedIPs:                  wireGuardE2EClientAddr + "/32",
		persistentKeepaliveInterval: 1,
	}})

	e.connectTunByPortName(f.clientWG, "left", "Tun", f.clientVTun, "right", "Tun")
	e.connectTunByPortName(f.serverWG, "left", "Tun", f.serverVTun, "right", "Tun")
	e.connectByPortName(f.mock, "left", "Network", f.clientWG, "right", "Network")
	e.connectByPortName(f.mock, "left", "Network", f.serverWG, "right", "Network")
	e.waitWireGuardUp(f.clientWG)
	e.waitWireGuardUp(f.serverWG)
	e.WaitNodeLabel(f.clientVTun, "wireguard-client-vtun")
	e.WaitNodeLabel(f.serverVTun, "wireguard-server-vtun")
	return f
}

type wireGuardPeerConfig struct {
	id                          string
	publicKey                   string
	presharedKey                string
	endpoint                    string
	allowedIPs                  string
	persistentKeepaliveInterval int
}

func applyWireGuardConfig(t *testing.T, engine Engine, node uint64, privateKey string, listenPort int, peers []wireGuardPeerConfig) {
	t.Helper()
	privateKeySecret := wireGuardTestPrivateKeySecret(t, engine, node, privateKey)
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "config",
		Values: map[string]any{
			"privateKeySecret": privateKeySecret,
			"listenPort":       listenPort,
			"batchSize":        0,
			"ownedIPs":         "",
		},
	})
	engine.SendNodeFormularMsg(E2EWorkspace, node, formular.FormApplyMessage{
		MessageBase: FormMessageBase(formular.MessageFormApply, node),
		BlockID:     "peers",
		Values:      map[string]any{"peers": wireGuardPeerValues(peers)},
	})
}

func wireGuardPeerValues(peers []wireGuardPeerConfig) []formular.ArrayElementValue {
	out := make([]formular.ArrayElementValue, 0, len(peers))
	for i, peer := range peers {
		id := strings.TrimSpace(peer.id)
		if id == "" {
			id = "peer-" + strconv.Itoa(i+1)
		}
		out = append(out, formular.ArrayElementValue{
			ID:       id,
			Template: "peer",
			Values: map[string]any{
				"publicKey":                   strings.TrimSpace(peer.publicKey),
				"presharedKey":                strings.TrimSpace(peer.presharedKey),
				"endpoint":                    strings.TrimSpace(peer.endpoint),
				"allowedIPs":                  strings.TrimSpace(peer.allowedIPs),
				"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
			},
		})
	}
	return out
}

func wireGuardTestPrivateKeySecret(t *testing.T, engine Engine, node uint64, privateKey string) string {
	t.Helper()
	storage := engine.SecretsStorage()
	if storage == nil {
		t.Fatal("test engine has no secrets storage")
	}
	snapshot, ok := engine.NodeSnapshot(E2EWorkspace, node)
	if !ok {
		t.Fatalf("NodeSnapshot(%d) missing", node)
	}
	name, err := storage.NewSecret(Secret{Value: privateKey}, snapshot.Name)
	if err != nil {
		t.Fatalf("NewSecret for WireGuard private key: %v", err)
	}
	return name
}

func wireGuardTestKeyPair(t *testing.T, seed byte) (privateKeyBase64, publicKeyBase64 string) {
	t.Helper()
	var privateKey wgdevice.NoisePrivateKey
	for i := range privateKey {
		privateKey[i] = seed + byte(i) + 1
	}
	privateKey[0] &= 248
	privateKey[31] = (privateKey[31] & 127) | 64
	publicKeyBase64 = privateKey.PublicKey().Base64()
	if publicKeyBase64 == "" {
		t.Fatal("wireguard public key is empty")
	}
	return privateKey.Base64(), publicKeyBase64
}
