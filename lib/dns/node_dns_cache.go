package dnsnodes

import (
	_ "embed"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect"
	"github.com/asciimoth/gonnect/dns"
	"github.com/asciimoth/pasta/pasta"
	"github.com/asciimoth/pasta/pasta/std"

	"github.com/asciimoth/almagest/lib/nodekit"
)

const (
	// NodeTypeDnsCache is the node class name for a manager-backed DNS cache
	// middleware.
	NodeTypeDnsCache = "almagest/DnsCache"
	// DNSCacheConfigBlockID is the Formular block ID for DnsCache config.
	DNSCacheConfigBlockID = "config"
	// DefaultDNSCacheStorageName is the fallback DNS cache storage name.
	DefaultDNSCacheStorageName = "default"
	// DNSCacheStorageNameField is the cache storage name field ID.
	DNSCacheStorageNameField = "storageName"
	// DNSCacheReverseLookupsField is the reverse lookup cache field ID.
	DNSCacheReverseLookupsField = "reverseLookups"
	// DNSCacheStorageWriteOnlyField is the write-only cache field ID.
	DNSCacheStorageWriteOnlyField = "writeOnly"

	defaultDnsCacheStorageName    = DefaultDNSCacheStorageName
	dnsCacheStorageNameField      = DNSCacheStorageNameField
	dnsCacheReverseLookupsField   = DNSCacheReverseLookupsField
	dnsCacheStorageWriteOnlyField = DNSCacheStorageWriteOnlyField
)

var (
	//go:embed node_dns_cache.md
	dnsCacheClassLongDescription string

	_ dnsAttachableCache = (*dns.Cache)(nil)

	dnsCacheNewCache = func(
		upstream dns.Interface,
		storage dns.CacheStorage,
		enableReverseLookups bool,
		storageWriteOnly bool,
		spawner gonnect.Spawner,
	) dnsAttachableCache {
		return dns.NewCache(upstream, storage, enableReverseLookups, storageWriteOnly, spawner)
	}
)

type dnsAttachableCache interface {
	dns.Interface
	Attach(dns.Interface)
	Detach()
}

// DNSCacheClass builds manager-backed DNS cache middleware nodes.
type DNSCacheClass struct {
	Runtime nodekit.Runtime
	Manager DnsCacheManager
}

func (DNSCacheClass) ClassName() string        { return NodeTypeDnsCache }
func (DNSCacheClass) ShortDescription() string { return "DNS cache" }
func (DNSCacheClass) LongDescription() string  { return dnsCacheClassLongDescription }
func (DNSCacheClass) DefaultNodeParams() pasta.NodeClassParams {
	return pasta.NodeClassParams{PrimaryType: TypeDNS, InitialPorts: []pasta.Port{
		DNSPort("left", "DNS"),
		DNSPort("right", "DNS"),
	}}
}
func (c DNSCacheClass) NewNode(cfg configer.Config, _ ...*pasta.NodeClassState) (pasta.Node, error) {
	config := defaultDNSCacheConfig()
	config.read(cfg)
	manager := c.Manager
	if manager == nil && c.Runtime != nil {
		manager = c.Runtime.DnsCacheManager()
	}
	return &dnsCacheNode{Base: nodekit.Base{Runtime: c.Runtime}, manager: manager, config: config}, nil
}

type dnsCacheConfig struct {
	storageName    string
	reverseLookups bool
	writeOnly      bool
}

func defaultDNSCacheConfig() dnsCacheConfig {
	return dnsCacheConfig{storageName: defaultDnsCacheStorageName}
}

func (c *dnsCacheConfig) read(cfg configer.Config) {
	if cfg == nil {
		return
	}
	c.storageName = configer.String(cfg, configer.Path{dnsCacheStorageNameField}, c.storageName)
	c.reverseLookups = configer.Bool(cfg, configer.Path{dnsCacheReverseLookupsField}, c.reverseLookups)
	c.writeOnly = configer.Bool(cfg, configer.Path{dnsCacheStorageWriteOnlyField}, c.writeOnly)
	c.normalize()
}

func (c *dnsCacheConfig) normalize() {
	c.storageName = strings.TrimSpace(c.storageName)
	if c.storageName == "" {
		c.storageName = defaultDnsCacheStorageName
	}
}

func (c dnsCacheConfig) validate() error {
	if !utf8.ValidString(c.storageName) {
		return errors.New("CacheStorage name must be valid UTF-8")
	}
	return nil
}

// dnsCacheNode is a DNS middleware node. It keeps the cache instance stable
// across input attach/detach so cached hits remain available without forcing
// downstream nodes to replace their current DNS wrapper.
type dnsCacheNode struct {
	nodekit.Base

	out uint64
	in  uint64

	inputLink uint64

	config  dnsCacheConfig
	manager DnsCacheManager

	upstreamDNS dns.Interface
	cache       dnsAttachableCache
	outputs     DNSOutputCache

	formError      string
	storageMissing bool
}

func (n *dnsCacheNode) OnInit(w *pasta.Workspace, logger pasta.Logger, id uint64, _ string, restored *pasta.NodeInitData, _, _, _, _ bool) error {
	n.InitBaseNode(w, logger, id)
	n.out = nodekit.RestoredLeft(restored, 0)
	n.in = nodekit.RestoredRight(restored, 0)
	if err := n.SetPrimary(TypeDNS); err != nil {
		return err
	}
	n.config.normalize()
	if err := n.config.validate(); err != nil {
		n.formError = err.Error()
	}
	n.rebuildCache()
	_ = n.updateLabel()
	n.sendMenuSnapshot()
	return nil
}

func (n *dnsCacheNode) OnReady() error {
	n.RequestPort(n.in)
	n.sendAll()
	return nil
}

func (n *dnsCacheNode) OnStop() {
	n.outputs.CloseAll()
	CloseDNS(n.cache)
	n.cache = nil
	CloseDNS(n.upstreamDNS)
	n.upstreamDNS = nil
	n.inputLink = 0
}

func (n *dnsCacheNode) PreLinkAdd(port uint64, linkType, portDirection string) error {
	switch port {
	case n.out:
		if portDirection != "left" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
	case n.in:
		if portDirection != "right" || linkType != TypeDNS {
			return pasta.LinkTypeErr(linkType)
		}
		if n.PortHasLinks(port) {
			return pasta.ErrLinkDup
		}
	default:
		return pasta.LinkTypeErr(linkType)
	}
	return nil
}

func (n *dnsCacheNode) OnLinkAdd(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.sendToLink(link)
	case port == n.in && portDirection == "right" && linkType == TypeDNS:
		n.inputLink = link
		std.RequestLocked(n.W, n.ID, link)
	}
	return n.updateLabel()
}

func (n *dnsCacheNode) OnLinkRemoved(link, port uint64, linkType, portDirection string) error {
	switch {
	case port == n.out && portDirection == "left" && linkType == TypeDNS:
		n.outputs.Close(link)
	case port == n.in && portDirection == "right" && linkType == TypeDNS && link == n.inputLink:
		if n.cache != nil {
			n.cache.Detach()
		}
		CloseDNS(n.upstreamDNS)
		n.upstreamDNS = nil
		n.inputLink = 0
	}
	return n.updateLabel()
}

func (n *dnsCacheNode) OnEvent(event pasta.Event, linkType string, _ []string, receiverPortDirection string) error {
	if event.ReceiverPort == n.out && receiverPortDirection == "left" && linkType == TypeDNS && std.IsRequest(event.Payload) {
		n.refreshLink(event.Link)
		return nil
	}
	if event.ReceiverPort != n.in || receiverPortDirection != "right" || linkType != TypeDNS || event.Link != n.inputLink {
		return nil
	}
	next, changed := AcceptDNSPayload(n.W, n.ID, event, n.upstreamDNS)
	if !changed {
		return nil
	}
	n.upstreamDNS = next
	if n.cache != nil {
		n.cache.Attach(next)
		n.sendAll()
	} else {
		n.rebuildCache()
	}
	return n.updateLabel()
}

func (n *dnsCacheNode) OnFormularMsg(message any) error {
	switch msg := message.(type) {
	case formular.FormApplyMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.BlockID != DNSCacheConfigBlockID {
			return nil
		}
		return n.applyConfigForm(msg.Values)
	case formular.FieldValidateMessage:
		if msg.MenuID != pasta.NodeMenuID(n.ID) || msg.Field.BlockID != DNSCacheConfigBlockID || len(msg.Field.ElementPath) > 0 {
			return nil
		}
		n.sendConfigFieldStatus(msg)
	}
	return nil
}

func (n *dnsCacheNode) OnSave(cfg configer.Config) error {
	if err := pasta.DeleteNodeOwnedConfigKeys(cfg); err != nil {
		return err
	}
	return configer.SetMap(cfg, map[string]any{
		dnsCacheStorageNameField:      n.config.storageName,
		dnsCacheReverseLookupsField:   n.config.reverseLookups,
		dnsCacheStorageWriteOnlyField: n.config.writeOnly,
	})
}

func (n *dnsCacheNode) applyConfigForm(values map[string]any) error {
	next := n.config
	if value, ok := formular.StringValue(values[dnsCacheStorageNameField]); ok {
		next.storageName = value
	}
	if value, ok := formular.BoolValue(values[dnsCacheReverseLookupsField]); ok {
		next.reverseLookups = value
	}
	if value, ok := formular.BoolValue(values[dnsCacheStorageWriteOnlyField]); ok {
		next.writeOnly = value
	}
	next.normalize()
	if err := next.validate(); err != nil {
		n.formError = err.Error()
		n.sendForcedMenuSnapshot()
		_ = n.updateLabel()
		return nil
	}

	changed := n.config != next || n.formError != ""
	n.formError = ""
	n.config = next
	n.sendForcedMenuSnapshot()
	if changed {
		n.rebuildCache()
	}
	return n.updateLabel()
}

func (n *dnsCacheNode) sendConfigFieldStatus(msg formular.FieldValidateMessage) {
	if msg.Field.FieldID != dnsCacheStorageNameField {
		nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, nil)
		return
	}
	next := n.config
	if value, ok := formular.StringValue(msg.Value); ok {
		next.storageName = value
	}
	next.normalize()
	nodekit.SendNodeFieldStatus(n.W, n.ID, msg.Field, next.validate())
}

func (n *dnsCacheNode) rebuildCache() {
	n.outputs.CloseAll()
	CloseDNS(n.cache)
	n.cache = nil
	n.storageMissing = false
	if n.formError != "" {
		return
	}
	manager := n.cacheManager()
	if manager == nil {
		return
	}
	storage := manager.Storage(n.config.storageName)
	if storage == nil {
		n.storageMissing = true
		return
	}
	cache := dnsCacheNewCache(n.upstreamDNS, storage, n.config.reverseLookups, n.config.writeOnly, n.dnsSpawner())
	n.cache = cache
	BindDNSResource(n.W, n.ID, 0, cache)
	n.sendAll()
}

func (n *dnsCacheNode) cacheManager() DnsCacheManager {
	return n.manager
}

func (n *dnsCacheNode) dnsSpawner() gonnect.Spawner {
	if n.WorkerSpawner == nil {
		return nil
	}
	return n.WorkerSpawner
}

func (n *dnsCacheNode) sendAll() {
	n.outputs.SendPortLocked(n.W, n.ID, n.out, n.cache)
}

func (n *dnsCacheNode) sendToLink(link uint64) {
	n.outputs.SendLocked(n.W, n.ID, link, n.cache)
}

func (n *dnsCacheNode) refreshLink(link uint64) {
	n.outputs.RefreshLocked(n.W, n.ID, link, n.cache)
}

func (n *dnsCacheNode) updateLabel() error {
	switch {
	case n.formError != "":
		return n.SetLabel("invalid config")
	case n.cacheManager() == nil:
		return n.SetLabel("no cache manager")
	case n.storageMissing:
		return n.SetLabel("no cache storage")
	case n.config.writeOnly && !DNSIsReady(n.upstreamDNS):
		return n.SetLabel("waiting for DNS")
	case !DNSIsReady(n.upstreamDNS):
		return n.SetLabel("cache-only DNS")
	case n.config.writeOnly:
		return n.SetLabel("warming DNS cache")
	default:
		return n.SetLabel("caching DNS")
	}
}

func (n *dnsCacheNode) sendMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, false, n.configBlock())
}

func (n *dnsCacheNode) sendForcedMenuSnapshot() {
	nodekit.SendNodeMenuSnapshot(n.W, n.ID, true, n.configBlock())
}

func (n *dnsCacheNode) configBlock() formular.Block {
	storageStatus := []formular.FieldOption{formular.Validation, formular.Placeholder(defaultDnsCacheStorageName)}
	if n.formError != "" {
		storageStatus = append(storageStatus, formular.Status(formular.StatusError), formular.StatusText(n.formError))
	}
	return formular.Block{ID: DNSCacheConfigBlockID, Order: 10, Generation: 1, Form: true, Items: []formular.Item{
		formular.TextField(dnsCacheStorageNameField, "CacheStorage name", n.config.storageName, storageStatus...),
		formular.CheckboxField(dnsCacheReverseLookupsField, "Reverse lookups", n.config.reverseLookups),
		formular.CheckboxField(dnsCacheStorageWriteOnlyField, "Write only", n.config.writeOnly),
	}}
}
