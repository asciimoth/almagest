package wireguardnodes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta/std"
	wgdevice "github.com/asciimoth/wgo/device"
)

type wireGuardConfig struct {
	privateKeySecret string
	// privateKey is the resolved key material used by the runtime. It is
	// populated from SecretsStorage and must never be serialized or shown in the
	// node menu.
	privateKey string
	// privateKeyLegacyValue carries a raw privateKey from pre-secrets configs
	// or old form clients just long enough to store it in SecretsStorage.
	privateKeyLegacyValue string
	listenPort            int
	batchSize             int
	ownedIPs              string
	amneziaWG             wireGuardAmneziaConfig
	peers                 []wireGuardPeerConfig
}
type wireGuardPeerConfig struct {
	id                 string
	publicKey          string
	presharedKeySecret string
	// presharedKey is runtime-only key material resolved from SecretsStorage.
	// Legacy config and old form clients may set it before secret migration.
	presharedKey                string
	endpoint                    string
	allowedIPs                  string
	persistentKeepaliveInterval int
	amneziaWGOverride           bool
	amneziaWG                   wireGuardAmneziaConfig
}

func defaultWireGuardConfig() wireGuardConfig {
	return wireGuardConfig{amneziaWG: defaultWireGuardAmneziaConfig()}
}
func (c *wireGuardConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.privateKeySecret = configer.String(cfg, configer.Path{"privateKeySecret"}, c.privateKeySecret)
	if c.privateKeySecret == "" {
		c.privateKeyLegacyValue = configer.String(cfg, configer.Path{"privateKey"}, c.privateKeyLegacyValue)
	}
	c.listenPort = configer.Int(cfg, configer.Path{"listenPort"}, c.listenPort)
	c.batchSize = configer.Int(cfg, configer.Path{"batchSize"}, c.batchSize)
	c.ownedIPs = configer.String(cfg, configer.Path{"ownedIPs"}, c.ownedIPs)
	c.amneziaWG = readWireGuardAmneziaConfig(cfg, c.amneziaWG)
	if peers, ok := readWireGuardPeers(cfg); ok {
		c.peers = peers
	}
	c.normalize()
}
func (c *wireGuardConfig) normalize() {
	c.privateKeySecret = strings.TrimSpace(c.privateKeySecret)
	c.privateKey = normalizeWireGuardKey(c.privateKey)
	c.privateKeyLegacyValue = normalizeWireGuardKey(c.privateKeyLegacyValue)
	if c.listenPort < 0 || c.listenPort > 65535 {
		c.listenPort = 0
	}
	if c.batchSize < 0 {
		c.batchSize = 0
	}
	c.ownedIPs = normalizeListText(c.ownedIPs)
	c.amneziaWG.normalize()
	for i := range c.peers {
		c.peers[i].normalize(i)
	}
	c.peers = compactWireGuardPeers(c.peers)
}

// clone makes form/validation edits independent from async runtime readers.
// The peer slice contains mutable structs, so a plain struct copy would let
// normalization race with a device apply still reading the previous config.
func (c wireGuardConfig) clone() wireGuardConfig {
	c.peers = slices.Clone(c.peers)
	return c
}
func (p *wireGuardPeerConfig) normalize(index int) {
	p.publicKey = normalizeWireGuardKey(p.publicKey)
	p.presharedKeySecret = strings.TrimSpace(p.presharedKeySecret)
	p.presharedKey = normalizeWireGuardKey(p.presharedKey)
	p.endpoint = strings.TrimSpace(p.endpoint)
	p.allowedIPs = normalizeListText(p.allowedIPs)
	p.amneziaWG.normalize()
	if p.persistentKeepaliveInterval < 0 || p.persistentKeepaliveInterval > 65535 {
		p.persistentKeepaliveInterval = 0
	}
	p.id = strings.TrimSpace(p.id)
	if p.id == "" {
		if p.publicKey != "" {
			p.id = "peer-" + shortWireGuardKey(p.publicKey)
		} else {
			p.id = fmt.Sprintf("peer-%d", index+1)
		}
	}
}
func applyWireGuardConfigValues(config *wireGuardConfig, values map[string]any) {
	if value, ok := formular.StringValue(values["privateKeySecret"]); ok {
		config.privateKeySecret = value
		config.privateKey = ""
		config.privateKeyLegacyValue = ""
	} else if value, ok := formular.StringValue(values["privateKey"]); ok {
		config.privateKeySecret = ""
		config.privateKey = ""
		config.privateKeyLegacyValue = value
	}
	if value, ok := formular.IntValue(values["listenPort"]); ok {
		config.listenPort = value
	}
	if value, ok := formular.IntValue(values["batchSize"]); ok {
		config.batchSize = value
	}
	if value, ok := formular.StringValue(values["ownedIPs"]); ok {
		config.ownedIPs = value
	}
	applyWireGuardAmneziaValues(&config.amneziaWG, values)
}
func validateWireGuardConfig(config wireGuardConfig) error {
	if config.privateKey != "" {
		if _, err := parseWireGuardPrivateKey(config.privateKey); err != nil {
			return fmt.Errorf("private key: %w", err)
		}
	}
	if config.listenPort < 0 || config.listenPort > 65535 {
		return errors.New("listen port must be between 0 and 65535")
	}
	if config.batchSize < 0 {
		return errors.New("batch size must be 0 or greater")
	}
	if _, err := parseWireGuardAllowedIPs(config.ownedIPs); err != nil {
		return fmt.Errorf("owned IPs: %w", err)
	}
	deviceConfig, err := wireGuardDeviceConfig(config)
	if err != nil {
		return err
	}
	return wgdevice.ValidateConfig(deviceConfig)
}
func parseWireGuardPrivateKey(value string) (wgdevice.NoisePrivateKey, error) {
	return wgdevice.ParsePrivateKey(strings.TrimSpace(value))
}
func generateWireGuardPrivateKeyBase64() (string, error) {
	key, err := wgdevice.GeneratePrivateKey()
	if err != nil {
		return "", err
	}
	return key.Base64(), nil
}
func parseWireGuardPublicKey(value string) (wgdevice.NoisePublicKey, error) {
	return wgdevice.ParsePublicKey(strings.TrimSpace(value))
}
func parseWireGuardPresharedKey(value string) (wgdevice.NoisePresharedKey, error) {
	return wgdevice.ParsePresharedKey(strings.TrimSpace(value))
}
func wireGuardPublicKeyBase64(privateKey wgdevice.NoisePrivateKey) string {
	if privateKey.IsZero() {
		return ""
	}
	return privateKey.PublicKey().Base64()
}
func parseWireGuardAllowedIPs(value string) ([]netip.Prefix, error) {
	fields := splitDefaultTunList(value)
	out := make([]netip.Prefix, 0, len(fields))
	for _, field := range fields {
		if prefix, err := netip.ParsePrefix(field); err == nil {
			out = append(out, prefix)
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP prefix", field)
		}
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

// wireGuardOwnedIPAddrs returns normalized CIDR strings suitable for TunConfig.
// A bare IP is expanded to a host prefix, and duplicates are removed while
// preserving the user's first-seen order.
func wireGuardOwnedIPAddrs(value string) []string {
	prefixes, err := parseWireGuardAllowedIPs(value)
	if err != nil {
		return nil
	}
	return wireGuardOwnedIPPrefixes(prefixes)
}

func normalizeWireGuardOwnedIPValues(values []string) ([]string, error) {
	prefixes, err := parseWireGuardAllowedIPs(strings.Join(values, "\n"))
	if err != nil {
		return nil, err
	}
	return wireGuardOwnedIPPrefixes(prefixes), nil
}

func wireGuardOwnedIPPrefixes(prefixes []netip.Prefix) []string {
	if len(prefixes) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		addr := prefix.String()
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

// wireGuardPeerRoutes returns normalized peer allowed-IP prefixes suitable for
// TunConfig route suggestions. Duplicates are removed while preserving peer
// order and each peer's allowed-IP order.
func wireGuardPeerRoutes(peers []wireGuardPeerConfig) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, peer := range peers {
		prefixes, err := parseWireGuardAllowedIPs(peer.allowedIPs)
		if err != nil {
			return nil
		}
		for _, prefix := range prefixes {
			route := prefix.String()
			if _, ok := seen[route]; ok {
				continue
			}
			seen[route] = struct{}{}
			out = append(out, route)
		}
	}
	return out
}
func normalizeWireGuardKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if key, err := wgdevice.ParsePresharedKey(value); err == nil {
		return key.Base64()
	}
	return value
}
func shortWireGuardKey(value string) string {
	value = normalizeWireGuardKey(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
func peerLabel(peer wireGuardPeerConfig) string {
	if peer.id != "" {
		return peer.id
	}
	if peer.publicKey != "" {
		return shortWireGuardKey(peer.publicKey)
	}
	return "peer"
}
func parseWireGuardPeers(raw any) ([]wireGuardPeerConfig, bool) {
	switch values := raw.(type) {
	case []formular.ArrayElementValue:
		return wireGuardPeersFromArray(values), true
	case []any:
		array := make([]formular.ArrayElementValue, 0, len(values))
		for _, rawElement := range values {
			element, ok := formular.ArrayElementValueFromAny(rawElement)
			if !ok {
				continue
			}
			array = append(array, element)
		}
		return wireGuardPeersFromArray(array), true
	default:
		return nil, false
	}
}
func wireGuardPeersFromArray(values []formular.ArrayElementValue) []wireGuardPeerConfig {
	out := make([]wireGuardPeerConfig, 0, len(values))
	for i, value := range values {
		if value.Template != "" && value.Template != "peer" {
			continue
		}
		peer := wireGuardPeerConfig{id: value.ID}
		peer.publicKey, _ = formular.StringValue(value.Values["publicKey"])
		peer.presharedKeySecret, _ = formular.StringValue(value.Values["presharedKeySecret"])
		if peer.presharedKeySecret == "" {
			peer.presharedKey, _ = formular.StringValue(value.Values["presharedKey"])
		}
		peer.endpoint, _ = formular.StringValue(value.Values["endpoint"])
		peer.allowedIPs, _ = formular.StringValue(value.Values["allowedIPs"])
		if keepalive, ok := formular.IntValue(value.Values["persistentKeepaliveInterval"]); ok {
			peer.persistentKeepaliveInterval = keepalive
		}
		if override, ok := formular.BoolValue(value.Values["amneziaOverride"]); ok {
			peer.amneziaWGOverride = override
		}
		applyWireGuardAmneziaValues(&peer.amneziaWG, value.Values)
		peer.normalize(i)
		out = append(out, peer)
	}
	return compactWireGuardPeers(out)
}
func compactWireGuardPeers(peers []wireGuardPeerConfig) []wireGuardPeerConfig {
	out := peers[:0]
	seenIDs := map[string]struct{}{}
	for i := range peers {
		peer := peers[i]
		peer.normalize(i)
		if peer.publicKey == "" && peer.presharedKeySecret == "" && peer.presharedKey == "" && peer.endpoint == "" && peer.allowedIPs == "" && peer.persistentKeepaliveInterval == 0 && !peer.amneziaWGOverride {
			continue
		}
		if _, ok := seenIDs[peer.id]; ok {
			peer.id = fmt.Sprintf("%s-%d", peer.id, i+1)
		}
		seenIDs[peer.id] = struct{}{}
		out = append(out, peer)
	}
	return out
}
func wireGuardPeersConfig(peers []wireGuardPeerConfig) []map[string]any {
	out := make([]map[string]any, 0, len(peers))
	for _, peer := range peers {
		out = append(out, map[string]any{
			"id":                          peer.id,
			"publicKey":                   peer.publicKey,
			"presharedKeySecret":          peer.presharedKeySecret,
			"endpoint":                    peer.endpoint,
			"allowedIPs":                  peer.allowedIPs,
			"persistentKeepaliveInterval": peer.persistentKeepaliveInterval,
			"amneziaWGOverride":           peer.amneziaWGOverride,
			"amneziaWG":                   wireGuardAmneziaConfigMap(peer.amneziaWG),
		})
	}
	return out
}
func readWireGuardPeers(cfg configer.Config) ([]wireGuardPeerConfig, bool) {
	raw, err := cfg.Get(configer.Path{"peers"})
	if err != nil {
		return nil, false
	}
	switch values := raw.(type) {
	case []map[string]any:
		out := make([]wireGuardPeerConfig, 0, len(values))
		for i, value := range values {
			out = append(out, wireGuardPeerFromConfigMap(i, value))
		}
		return compactWireGuardPeers(out), true
	case []any:
		out := make([]wireGuardPeerConfig, 0, len(values))
		for i, rawValue := range values {
			if value, ok := rawValue.(map[string]any); ok {
				out = append(out, wireGuardPeerFromConfigMap(i, value))
			}
		}
		return compactWireGuardPeers(out), true
	default:
		return nil, false
	}
}

// wireGuardPeersFromObject reads the peer list carried by a pasta/object
// input. The object has the same {"peers": [...]} shape as WireGuard node
// config. Other top-level fields are ignored so a producer can add metadata
// without changing the WireGuard contract.
func wireGuardPeersFromObject(object std.Object) ([]wireGuardPeerConfig, error) {
	data, err := object.JSON()
	if err != nil {
		return nil, fmt.Errorf("encode object: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode object: %w", err)
	}
	if root == nil {
		return nil, errors.New("object must contain a peers array")
	}
	rawPeers, ok := root["peers"]
	if !ok {
		return nil, errors.New("object must contain a peers array")
	}
	values, ok := rawPeers.([]any)
	if !ok {
		return nil, errors.New("object peers must be an array")
	}
	peers := make([]wireGuardPeerConfig, 0, len(values))
	for i, rawPeer := range values {
		value, ok := rawPeer.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("object peer %d must be an object", i+1)
		}
		peers = append(peers, wireGuardPeerFromConfigMap(i, value))
	}
	return compactWireGuardPeers(peers), nil
}

// mergeWireGuardPeers combines the latest peer list from each object link with
// the node menu peer list. A WireGuard public key is the peer identity. Menu
// peers are added first and suppress object peers with the same normalized key,
// which gives the menu the required conflict priority. Object links are read in
// stable link-ID order; the lower link ID wins conflicts between object links.
func mergeWireGuardPeers(menu []wireGuardPeerConfig, byLink map[uint64][]wireGuardPeerConfig) []wireGuardPeerConfig {
	out := slices.Clone(menu)
	seen := make(map[string]struct{}, len(menu))
	for _, peer := range menu {
		seen[normalizeWireGuardKey(peer.publicKey)] = struct{}{}
	}

	links := make([]uint64, 0, len(byLink))
	for link := range byLink {
		links = append(links, link)
	}
	slices.Sort(links)
	for _, link := range links {
		for _, peer := range byLink[link] {
			key := normalizeWireGuardKey(peer.publicKey)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, peer)
		}
	}
	return out
}

func wireGuardPeerFromConfigMap(index int, value map[string]any) wireGuardPeerConfig {
	peer := wireGuardPeerConfig{}
	peer.id, _ = formular.StringValue(value["id"])
	peer.publicKey, _ = formular.StringValue(value["publicKey"])
	peer.presharedKeySecret, _ = formular.StringValue(value["presharedKeySecret"])
	if peer.presharedKeySecret == "" {
		peer.presharedKey, _ = formular.StringValue(value["presharedKey"])
	}
	peer.endpoint, _ = formular.StringValue(value["endpoint"])
	peer.allowedIPs, _ = formular.StringValue(value["allowedIPs"])
	if keepalive, ok := formular.IntValue(value["persistentKeepaliveInterval"]); ok {
		peer.persistentKeepaliveInterval = keepalive
	}
	if override, ok := formular.BoolValue(value["amneziaWGOverride"]); ok {
		peer.amneziaWGOverride = override
	}
	peer.amneziaWG = readWireGuardAmneziaConfigMap(value["amneziaWG"], peer.amneziaWG)
	peer.normalize(index)
	return peer
}
