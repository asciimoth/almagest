package yggdrasilnodes

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/asciimoth/almagest/lib/links"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/tun"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"
)

// NodeTypeYggdrasil is the embedded Yggdrasil node class.
const NodeTypeYggdrasil = "almagest/Yggdrasil"

// YggdrasilClass creates Yggdrasil mesh nodes.
type YggdrasilClass struct{ Runtime nodekit.Runtime }

func (YggdrasilClass) ClassName() string        { return NodeTypeYggdrasil }
func (YggdrasilClass) ShortDescription() string { return "Yggdrasil" }
func (YggdrasilClass) LongDescription() string {
	return "Connects a TUN to the Yggdrasil mesh through selected upstream networks."
}
func (YggdrasilClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: links.TypeTun, InitialPorts: []pasta.Port{links.TunPort("left", "Tun"), nodekit.NetworkPort("right", "default")}}
}
func (c YggdrasilClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := readConfig(cfg)
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &node{Base: nodekit.Base{Runtime: c.Runtime}, config: config, ports: map[uint64]string{}, networks: map[string]nodekit.NetworkCloser{}, networkLinks: map[uint64]uint64{}, updates: make(chan desired, 1), stop: make(chan struct{}), done: make(chan struct{})}, nil
}

// Classes returns the built-in Yggdrasil classes.
func Classes(runtime nodekit.Runtime) []pasta.NodeClass {
	return []pasta.NodeClass{YggdrasilClass{Runtime: runtime}}
}

// Workspace callbacks own desired state. One worker owns all Ygg runtime calls.
// The buffered channel keeps only the newest complete snapshot. Results carry
// its generation, so an old completion cannot publish readiness after unlink.
type node struct {
	nodekit.Base
	config                                                  configuration
	key                                                     ed25519.PrivateKey
	tunPort, tunLink                                        uint64
	device                                                  tun.Tun
	ports                                                   map[uint64]string
	networkLinks                                            map[uint64]uint64
	networks                                                map[string]nodekit.NetworkCloser
	generation                                              uint64
	updates                                                 chan desired
	stop, done                                              chan struct{}
	stopped                                                 bool
	address, state, problem                                 string
	connectedPeers, autoPeerCandidates, linkLocalInterfaces int
	jumperActive                                            bool
}

func (n *node) OnInit(w *pasta.Workspace, l pasta.Logger, id uint64, _ string, r *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, l, id)
	n.tunPort = nodekit.RestoredLeft(r, 0)
	if err := n.SetPrimary(links.TypeTun); err != nil {
		return err
	}
	if err := n.rebuildPorts(); err != nil {
		return err
	}
	if err := n.resolveKey(); err != nil {
		n.problem = err.Error()
	}
	if err := n.SpawnWorker("Yggdrasil runtime", n.run); err != nil {
		return err
	}
	n.menu()
	return nil
}
func (n *node) resolveKey() error {
	store := n.SecretsStorage()
	if store == nil {
		return errors.New("secrets storage is unavailable")
	}
	name := n.Name
	if snapshot, ok := n.W.NodeSnapshotLocked(n.ID); ok {
		name = snapshot.Name
	}
	if n.config.secret == "" {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		secret, err := store.NewSecret(storage.Secret{Value: hex.EncodeToString(key)}, name)
		if err != nil {
			return err
		}
		n.config.secret = secret
	}
	sec, err := store.GetSecret(n.config.secret, name)
	if err != nil {
		return err
	}
	key, err := hex.DecodeString(strings.TrimSpace(sec.Value))
	if err != nil {
		return errors.New("identity secret must contain a hexadecimal Ed25519 key")
	}
	if len(key) == ed25519.SeedSize {
		key = ed25519.NewKeyFromSeed(key)
	}
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("identity secret must contain 32 or 64 bytes")
	}
	// Reject inconsistent public halves in imported private keys.
	canonical := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	if !canonical.Equal(ed25519.PrivateKey(key)) {
		return errors.New("identity secret has an inconsistent public key")
	}
	n.key = canonical
	return nil
}
func (n *node) OnReady() error {
	n.schedule()
	n.RequestPort(n.tunPort)
	for port := range n.ports {
		n.RequestPort(port)
	}
	return nil
}
func (n *node) OnStop() {
	if !n.stopped {
		n.stopped = true
		n.generation++
		close(n.stop)
	}
}
func (n *node) rebuildPorts() error {
	wanted := n.config.mappings()
	previousNetworks := maps.Clone(n.networks)
	nextNetworks := map[string]nodekit.NetworkCloser{}
	snapshot, ok := n.W.NodeSnapshotLocked(n.ID)
	if !ok {
		return pasta.ErrNoNode
	}
	for _, id := range snapshot.RightPorts {
		p, ok := n.W.PortSnapshotLocked(id)
		if !ok {
			continue
		}
		if wanted[p.Name] == "" {
			n.W.RemovePortLocked(id)
			delete(n.ports, id)
			continue
		}
		if network, exists := previousNetworks[n.ports[id]]; exists {
			nextNetworks[wanted[p.Name]] = network
		}
		n.ports[id] = wanted[p.Name]
		delete(wanted, p.Name)
	}
	for name, pattern := range wanted {
		p := nodekit.NetworkPort("right", name)
		p.Node = n.ID
		id, err := n.W.AddPortLocked(p)
		if err != nil {
			return err
		}
		n.ports[id] = pattern
	}
	n.networks = nextNetworks
	return nil
}
func (n *node) PreLinkAdd(port uint64, typ, direction string) error {
	if port == n.tunPort && direction == "left" {
		return links.TunPreLinkAdd(n.W, port, typ)
	}
	if _, ok := n.ports[port]; ok && direction == "right" && typ == links.TypeNetwork {
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
		return nil
	}
	return pasta.LinkTypeErr(typ)
}
func (n *node) OnLinkAdd(link, port uint64, _, _ string) error {
	if port == n.tunPort {
		n.tunLink = link
		n.sendTunConfig()
	} else {
		n.networkLinks[port] = link
	}
	std.RequestLocked(n.W, n.ID, link)
	return nil
}
func (n *node) OnLinkRemoved(link, port uint64, _, _ string) error {
	if port == n.tunPort && n.tunLink == link {
		n.tunLink = 0
		n.device = nil
	} else if n.networkLinks[port] == link {
		delete(n.networkLinks, port)
		delete(n.networks, n.ports[port])
	}
	n.schedule()
	return nil
}
func (n *node) OnEvent(e pasta.Event, typ string, _ []string, direction string) error {
	if e.ReceiverPort == n.tunPort && direction == "left" && typ == links.TypeTun && e.Link == n.tunLink {
		if std.IsRequest(e.Payload) {
			n.sendTunConfig()
			return nil
		}
		next, changed := links.AcceptTunPayload(n.W, n.ID, e, n.tunLink, n.device)
		if changed {
			n.device = next
			n.schedule()
		}
		return nil
	}
	if pattern, ok := n.ports[e.ReceiverPort]; ok && direction == "right" && typ == links.TypeNetwork && n.networkLinks[e.ReceiverPort] == e.Link {
		next, changed := nodekit.AcceptNetworkPayload(n.W, n.ID, e, n.networks[pattern])
		if changed {
			n.networks[pattern] = next
			n.schedule()
		}
	}
	return nil
}
func (n *node) schedule() {
	if n.stopped {
		return
	}
	n.generation++
	d := desired{generation: n.generation, config: n.config, key: n.key, device: n.device, networks: maps.Clone(n.networks)}
	for _, pattern := range n.config.mappings() {
		if _, ok := d.networks[pattern]; !ok {
			d.networks[pattern] = nil
		}
	}
	select {
	case <-n.updates:
	default:
	}
	n.updates <- d
	n.state = "starting"
	_ = n.SetLabel(n.state)
}
func (n *node) OnInbox(msg pasta.InboxMessage) error {
	switch r := msg.Payload.(type) {
	case result:
		if n.stopped || r.generation != n.generation {
			return nil
		}
		n.state = r.state
		n.address = r.address
		n.connectedPeers = r.connectedPeers
		n.autoPeerCandidates = r.autoPeerCandidates
		n.linkLocalInterfaces = r.linkLocalInterfaces
		n.jumperActive = r.jumperActive
		if n.problem == "" {
			_ = n.SetLabel(r.state)
		} else {
			_ = n.SetLabel("error: " + n.problem)
		}
		n.sendTunConfig()
		n.menu()
		if r.requestNetwork {
			for port, pattern := range n.ports {
				if network := n.networks[pattern]; network != nil && !links.NetworkIsReady(network) {
					n.RequestPort(port)
				}
			}
		}
		if r.requestTun {
			n.device = nil
			n.RequestPort(n.tunPort)
		}
	case storage.SecretValueChanged:
		if n.config.secret != r.SecretNamePrev && n.config.secret != r.SecretNameNew {
			return nil
		}
		if r.SecretNamePrev != r.SecretNameNew && r.SecretNameNew != "" {
			n.config.secret = r.SecretNameNew
			_ = n.NotifyConfigChanged()
		}
		n.key = nil
		n.problem = ""
		if err := n.resolveKey(); err != nil {
			n.problem = err.Error()
		}
		n.schedule()
		n.menu()
	}
	return nil
}
func (n *node) sendTunConfig() {
	if n.address != "" {
		n.EmitIfLinked(n.tunLink, links.TunConfig{Addrs: []string{n.address + "/128"}, Routes: []string{"200::/7"}, MTU: 65535})
	}
}
func (n *node) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return nodekit.SetConfigMap(cfg, n.config.values())
}
func (n *node) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != "config" {
			return nil
		}
		next := n.config.apply(msg.Values)
		if err := next.validate(); err != nil {
			n.problem = err.Error()
			n.menu()
			return nil
		}
		if next == n.config {
			n.problem = ""
			n.menu()
			return nil
		}
		previous := n.config
		key := n.key
		n.config = next
		if err := n.resolveKey(); err != nil {
			n.config = previous
			n.key = key
			n.problem = err.Error()
			n.menu()
			return nil
		}
		n.problem = ""
		if err := n.rebuildPorts(); err != nil {
			return err
		}
		n.schedule()
		n.menu()
	case formular.FieldValidateMessage:
		if msg.MenuID == pasta.NodeMenuID(n.ID) && msg.Field.BlockID == "config" {
			nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, n.config.apply(map[string]any{msg.Field.FieldID: msg.Value}).validate())
		}
	}
	return nil
}
func (n *node) menu() {
	jumperState := "off"
	if n.jumperActive {
		jumperState = "on"
	}
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.config.block(n.problem), formular.Block{ID: "status", Order: 20, Generation: 1, Items: []formular.Item{
		nodekit.ReadonlyTextField("state", "State", n.state),
		nodekit.ReadonlyTextField("address", "IPv6 address", n.address),
		nodekit.ReadonlyTextField("connectedPeers", "Connected peers", fmt.Sprint(n.connectedPeers)),
		nodekit.ReadonlyTextField("autoPeerCandidates", "Public-peer candidates", fmt.Sprint(n.autoPeerCandidates)),
		nodekit.ReadonlyTextField("linkLocalInterfaces", "Link-local interfaces", fmt.Sprint(n.linkLocalInterfaces)),
		nodekit.ReadonlyTextField("jumper", "NodeInfo jumper", jumperState),
	}})
}
