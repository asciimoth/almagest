package almagest

import (
	"context"
	"errors"
	"io"
	"maps"
	"sync"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/subnet"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"

	amnesianodes "github.com/asciimoth/almagest/lib/amnesia"
	"github.com/asciimoth/almagest/lib/logging"
	"github.com/asciimoth/almagest/lib/nodekit"
	"github.com/asciimoth/almagest/lib/storage"
)

var (
	// Static type assertion
	_ Engine = &engine{}
)

var (
	ErrNoWorkspace = errors.New("no such workspace")
)

// MainWorkspace is the engine workspace name used by the public API.
const MainWorkspace = "main"

// BuildEngineOption changes optional engine build behavior.
type BuildEngineOption func(*buildEngineOptions)

type buildEngineOptions struct {
	logWorkerEvents          bool
	nodeNameAvailable        pasta.NodeNameAvailableCallback
	nodeConfigChangeCallback func(uint64) <-chan error
	randomSeed               *int64
}

// WithWorkerEventLogging controls engine worker lifecycle logs.
func WithWorkerEventLogging(enabled bool) BuildEngineOption {
	return func(options *buildEngineOptions) {
		options.logWorkerEvents = enabled
	}
}

// WithNodeNameAvailableCallback sets the optional callback that checks names
// generated for new workspace nodes.
func WithNodeNameAvailableCallback(callback pasta.NodeNameAvailableCallback) BuildEngineOption {
	return func(options *buildEngineOptions) {
		options.nodeNameAvailable = callback
	}
}

// WithNodeConfigChangeCallback sets the callback for node configuration changes.
// The returned channel must report the result after the changed configuration
// is durable. It must send one error value and then close.
func WithNodeConfigChangeCallback(callback func(node uint64) <-chan error) BuildEngineOption {
	return func(options *buildEngineOptions) {
		options.nodeConfigChangeCallback = callback
	}
}

// WithRandomSeed sets the seed for pseudo-random generators owned by the
// engine. If this option is not set, each generator keeps its deterministic
// default seed.
func WithRandomSeed(seed int64) BuildEngineOption {
	return func(options *buildEngineOptions) {
		options.randomSeed = &seed
	}
}

type NotificationKind string

const (
	// NotificationWorkspace carries a wrapped workspace notification
	NotificationWorkspace NotificationKind = "workspace"
	NotificationColorMap  NotificationKind = "colormap"
	NotificationWorker    NotificationKind = "worker"
)

// EngineNotification describes one observable engine state change.
type EngineNotification struct {
	SubscriptionID uint64 `json:"-"`

	Kind NotificationKind `json:"kind"`

	// WorkspaceID is an ID of workspace sent notification.
	// Currently there is only "main" one.
	WorkspaceID string                       `json:"workspace_id,omitempty"`
	Workspace   *pasta.WorkspaceNotification `json:"workspace_notification,omitempty"`
	Worker      *WorkerNotification          `json:"worker_notification,omitempty"`

	ColorMap map[string]TypePalette `json:"colormap,omitempty"`
}

type NotificationCallback func(EngineNotification)

// EngineNotificationTransaction groups workspace and worker changes into one
// observable engine-state replacement. Publish is idempotent. The outermost
// active transaction schedules one full workspace snapshot and one full worker
// snapshot, then resumes incremental notifications. Publication can finish
// after Publish returns when an older notification callback is still running.
//
// A transaction changes notification delivery only. It does not lock the
// workspace or make the caller's state mutations transactional. Callers must
// use their own mutation lock and must always call Publish, usually with defer.
type EngineNotificationTransaction interface {
	Publish()
}

func BuildEngine(
	ctx context.Context,
	cfg configer.Config, // may be nil
	logf logging.LogFactory,
	sys sysnet.System, // use sysnetdebug.System{} if you dont have one
	kv storage.KVManager, // may be nil
	dns storage.DnsCacheManager, // may be nil
	secrets storage.SecretsStorage, // may be nil
	tlsConfigs storage.TlsConfigStorage, // may be nil
	extra []pasta.NodeClass,
	pool bufpool.Pool, // may be nil. Use bufpool.NewTestDebugPool() in tests
	debug bool, // Should builtin debug nodes be added
	options ...BuildEngineOption,
) (Engine, error) {
	ctx, cancel := context.WithCancel(ctx)
	buildOptions := buildEngineOptions{}
	for _, option := range options {
		if option != nil {
			option(&buildOptions)
		}
	}

	engine := &engine{
		ctx:                      ctx,
		ctxCancel:                cancel,
		logf:                     logf,
		logger:                   logf.EngineLogger(),
		pool:                     pool,
		sys:                      sys,
		kv:                       kv,
		dns:                      dns,
		secrets:                  secrets,
		tlsConfigs:               tlsConfigs,
		mainWorkspace:            nil,
		nextSubscriptionID:       100, // to avoid collision between our IDs and workspace ones
		subscriptionIDMapping:    make(map[uint64]subMap),
		notificationCallbacks:    make(map[uint64]NotificationCallback),
		notificationWorkerFloor:  make(map[uint64]uint64),
		nodeConfigChangeCallback: buildOptions.nodeConfigChangeCallback,
	}
	engine.workers = newWorkerTracker(func() *pasta.Workspace {
		return engine.mainWorkspace
	}, engine.logger, buildOptions.logWorkerEvents)
	engine.workers.onFailure = engine.handleWorkerFailure

	classes := nodeClasses(engine, debug)
	if len(extra) > 0 {
		for _, class := range extra {
			if bound, ok := class.(nodekit.RuntimeNodeClass); ok {
				class = bound.BindRuntime(engine)
			}
			classes = append(classes, class)
		}
	}

	var nodesCfg configer.Config
	if cfg != nil {
		nodesCfg = cfg.View(configer.Path{"nodes"})
	}

	work, err := pasta.WorkspaceFromConfig(
		classes,
		nodesCfg,
		logf,
	)

	if err != nil {
		cancel()
		return nil, err
	}
	if buildOptions.randomSeed != nil {
		work.SetNodeNameRandomSeed(*buildOptions.randomSeed)
	}
	work.SetNodeNameAvailableCallback(buildOptions.nodeNameAvailable)

	engine.mainWorkspace = work
	engine.configCommentPopups = initialConfigCommentPopups(nodesCfg, work.Snapshot())
	engine.workspaceObserverSubscription = work.SubscribeNotifications(func(notification pasta.WorkspaceNotification) {
		engine.workers.observeWorkspaceNotification(notification)
		engine.observeWorkspaceBarrier(0, notification)
	})

	return engine, nil
}

// pasta.Workspace methods re-exported by Engine
type PastaAPI interface {
	RemoveNode(workspace string, id uint64)
	RemoveNodeLocked(workspace string, id uint64)
	// RemoveNodes removes every existing node in ids as one workspace
	// mutation. Missing, duplicate, and invalid node IDs are ignored.
	RemoveNodes(workspace string, ids []uint64)
	RemoveNodesLocked(workspace string, ids []uint64)
	// RemoveNodesByClass removes all nodes with class.
	RemoveNodesByClass(workspace string, class string)
	RemoveNodesByClassLocked(workspace string, class string)
	// RemoveLink removes a link by it's id
	RemoveLink(workspace string, id uint64)
	RemoveLinkLocked(workspace string, id uint64)

	WorkspaceSnapshot(workspace string) (pasta.WorkspaceSnapshot, bool)
	WorkspaceSnapshotLocked(workspace string) (pasta.WorkspaceSnapshot, bool)
	NodeClassSnapshot(workspace string, name string) (pasta.NodeClassSnapshot, bool)
	NodeClassSnapshotLocked(workspace string, name string) (pasta.NodeClassSnapshot, bool)
	NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool)
	NodeSnapshotLocked(workspace string, id uint64) (pasta.NodeSnapshot, bool)
	PortSnapshot(workspace string, id uint64) (pasta.PortSnapshot, bool)
	PortSnapshotLocked(workspace string, id uint64) (pasta.PortSnapshot, bool)
	LinkSnapshot(workspace string, id uint64) (pasta.LinkSnapshot, bool)
	LinkSnapshotLocked(workspace string, id uint64) (pasta.LinkSnapshot, bool)

	NodeClassLongDescription(workspace string, name string) (string, bool)
	NodeClassLongDescriptionLocked(workspace string, name string) (string, bool)

	// Add node by it's class name
	AddNode(workspace string, class string, name string) (uint64, error)
	AddNodeLocked(workspace string, class string, name string) (uint64, error)
	// ReplaceNodeWithPlaceholder replaces an existing node implementation with a
	// placeholder record while preserving existing ports and links.
	ReplaceNodeWithPlaceholder(workspace string, id uint64) error
	ReplaceNodeWithPlaceholderLocked(workspace string, id uint64) error
	// AddLink connects two ports and returns the new link ID and selected link type.
	AddLink(workspace string, pa, pb uint64) (uint64, string, error)
	AddLinkLocked(workspace string, pa, pb uint64) (uint64, string, error)

	// NodeIDByName returns the workspace ID for a node name.
	NodeByName(workspace string, name string) (uint64, bool)
	NodeByNameLocked(workspace string, name string) (uint64, bool)

	// PortsConnected reports whether two ports are connected by a link.
	PortsConnected(workspace string, pa, pb uint64) bool
	PortsConnectedLocked(workspace string, pa, pb uint64) bool
	// NodesConnected reports whether two nodes have at least one direct link.
	NodesConnected(workspace string, na, nb uint64) bool
	NodesConnectedLocked(workspace string, na, nb uint64) bool
	// LinkByPorts returns the link connecting two ports.
	LinkByPorts(workspace string, pa, pb uint64) (uint64, pasta.LinkSnapshot, bool)
	LinkByPortsLocked(workspace string, pa, pb uint64) (uint64, pasta.LinkSnapshot, bool)
	LinksByNodes(workspace string, na, nb uint64) map[uint64]pasta.LinkSnapshot
	LinksByNodesLocked(workspace string, na, nb uint64) map[uint64]pasta.LinkSnapshot

	SetNodePosition(workspace string, id uint64, position string)
	SetNodePositionLocked(workspace string, id uint64, position string)
	// MoveNodes sets opaque frontend position strings for existing nodes as one
	// workspace mutation. Missing and invalid node IDs are ignored.
	MoveNodes(workspace string, positions map[uint64]string)
	MoveNodesLocked(workspace string, positions map[uint64]string)
	SetNodeName(workspace string, id uint64, name string)
	SetNodeNameLocked(workspace string, id uint64, name string)

	RemoveNodePopup(workspace string, id, popupID uint64)
	RemoveNodePopupLocked(workspace string, id, popupID uint64)
	RemoveNodePopups(workspace string, id uint64)
	RemoveNodePopupsLocked(workspace string, id uint64)
	RemoveNodePopupsByType(workspace string, id uint64, popupType string)
	RemoveNodePopupsByTypeLocked(workspace string, id uint64, popupType string)

	// Copy serializes the selected nodes and links between them into an opaque
	// clipboard string.
	// Missing node IDs and duplicate selections are ignored. Links are included
	// only when both endpoint nodes are selected. If any selected live node fails
	// OnSave, Copy returns an empty string.
	Copy(workspace string, ids []uint64) string
	CopyLocked(workspace string, ids []uint64) string
	// Paste restores a clipboard string as new nodes and returns their new IDs.
	// Invalid clipboard data and individual node/link restore failures are ignored.
	// Available class factories may recreate live nodes; otherwise placeholders are
	// created so copied graph structure can still be restored.
	Paste(workspace string, data string) []uint64
	PasteLocked(workspace string, data string) []uint64

	// SendInbox schedules delivery of message to its receiver node.
	SendInbox(workspace string, message pasta.InboxMessage)
	SendInboxLocked(workspace string, message pasta.InboxMessage)
	// Trigger invokes a node's explicit OnTrigger callback.
	Trigger(workspace string, id uint64) error
	TriggerLocked(workspace string, id uint64) error

	Undo(workspace string)
	UndoLocked(workspace string)
	Redo(workspace string)
	RedoLocked(workspace string)
}

type Engine interface {
	io.Closer
	PastaAPI

	CloseLocked() error

	// Lock locks engine-owned state. It is not recursive.
	//
	// Callers running from pasta node or notification callbacks should prefer
	// the Locked method variants instead of entering public methods that may
	// acquire workspace locks again.
	Lock()
	Unlock()

	// Saves internal pasta.Workspace config under `nodes` key
	SaveConfig(cfg configer.Config) error
	SaveConfigLocked(cfg configer.Config) error

	// ReconcileConfig applies the `nodes` section of cfg to the live workspace.
	// Nodes and links whose saved configuration did not change keep their
	// workspace IDs and runtime resources. The method validates the document
	// structure before it starts. A runtime lifecycle error can stop the apply
	// after partial work, so callers that require atomic behavior must reconcile
	// their previous config after an error.
	ReconcileConfig(cfg configer.Config) (ConfigReconcileResult, error)

	AllocIP() subnet.IPAllocator
	AllocIPLocked() subnet.IPAllocator
	AllocSubnet() subnet.SubnetAllocator
	AllocSubnetLocked() subnet.SubnetAllocator

	// SubscribeNotifications subscribes callback to engine notifications.
	// The callback receives a full workspace snapshot before SubscribeNotifications
	// returns. The returned subscription ID can be passed to
	// UnsubscribeNotifications.
	SubscribeNotifications(callback NotificationCallback) uint64
	SubscribeNotificationsLocked(callback NotificationCallback) uint64
	UnsubscribeNotifications(id uint64)
	UnsubscribeNotificationsLocked(id uint64)
	RequestFullSnapshot(subscriptionID uint64)
	RequestFullSnapshotLocked(subscriptionID uint64)

	// BeginNotificationTransaction suppresses incremental workspace and worker
	// notifications until Publish is called. The outermost transaction schedules
	// the complete resulting state before later incremental events are delivered.
	// Publication does not block Publish while an older callback is still active.
	BeginNotificationTransaction() EngineNotificationTransaction

	// Currently only "main" workspace available
	SubscribeNodeMenu(workspace string, node, subscriptionID uint64)
	SubscribeNodeMenuLocked(workspace string, node, subscriptionID uint64)
	UnsubscribeNodeMenu(workspace string, node, subscriptionID uint64)
	UnsubscribeNodeMenuLocked(workspace string, node, subscriptionID uint64)

	SendNodeFormularMsg(workspace string, node uint64, message any)
	SendNodeFormularMsgLocked(workspace string, node uint64, message any)

	WorkersSnapshot(workspace string) (map[uint64]WorkerSnapshot, bool)
	WorkersSnapshotLocked(workspace string) (map[uint64]WorkerSnapshot, bool)
	SubscribeWorkerNotifications(callback WorkerNotificationCallback) uint64
	SubscribeWorkerNotificationsLocked(callback WorkerNotificationCallback) uint64
	UnsubscribeWorkerNotifications(id uint64)
	UnsubscribeWorkerNotificationsLocked(id uint64)
	RequestWorkerSnapshot(subscriptionID uint64)
	RequestWorkerSnapshotLocked(subscriptionID uint64)

	Pool() bufpool.Pool
	PoolLocked() bufpool.Pool

	KVManager() storage.KVManager
	DnsCacheManager() storage.DnsCacheManager
	SecretsStorage() storage.SecretsStorage
	TlsConfigStorage() storage.TlsConfigStorage
}

type subMap struct {
	MainWorkspace uint64
	Workers       uint64
}

type engine struct {
	ctx       context.Context
	ctxCancel context.CancelFunc

	logf   logging.LogFactory
	logger logging.Logger

	pool bufpool.Pool

	sys        sysnet.System
	kv         storage.KVManager
	dns        storage.DnsCacheManager
	secrets    storage.SecretsStorage
	tlsConfigs storage.TlsConfigStorage

	mu sync.Mutex

	configReconcileMu   sync.Mutex
	configCommentPopups map[string]configCommentPopup

	mainWorkspace *pasta.Workspace
	workers       *workerTracker

	nextSubscriptionID uint64
	// mapping from engine notification subscription IDs to internal
	subscriptionIDMapping map[uint64]subMap

	// notificationMu protects the publication transaction state. It is separate
	// from mu because callbacks can re-enter normal engine methods.
	notificationMu                sync.Mutex
	notificationCallbacks         map[uint64]NotificationCallback
	notificationWorkerFloor       map[uint64]uint64
	notificationTransactionDepth  int
	notificationFinishing         bool
	notificationPublishing        bool
	notificationRepublish         bool
	notificationQueue             []engineNotificationDelivery
	workspaceObserverSubscription uint64
	workspaceBarrier              chan struct{}
	workspaceBarrierPending       map[uint64]struct{}

	nodeConfigChangeCallback func(uint64) <-chan error
}

func (e *engine) Lock() {
	e.mu.Lock()
}

func (e *engine) Unlock() {
	e.mu.Unlock()
}

func (e *engine) Pool() bufpool.Pool {
	return e.pool
}

func (e *engine) PoolLocked() bufpool.Pool {
	return e.pool
}

func (e *engine) KVManager() storage.KVManager {
	return e.kv
}

func (e *engine) DnsCacheManager() storage.DnsCacheManager {
	return e.dns
}

func (e *engine) SecretsStorage() storage.SecretsStorage {
	return e.secrets
}

func (e *engine) TlsConfigStorage() storage.TlsConfigStorage {
	return e.tlsConfigs
}

func (e *engine) Close() error {
	return e.CloseLocked()
}

func (e *engine) CloseLocked() error {
	e.ctxCancel()
	if e.mainWorkspace != nil {
		e.mainWorkspace.Close()
	}
	if e.workers != nil {
		e.workers.close()
	}
	// TODO: Close other workspaces
	return e.sys.Close()
}

func (e *engine) AllocIP() subnet.IPAllocator {
	return e.sys.AllocIP()
}

func (e *engine) AllocIPLocked() subnet.IPAllocator {
	return e.sys.AllocIP()
}

func (e *engine) AllocSubnet() subnet.SubnetAllocator {
	return e.sys.AllocSubnet()
}

func (e *engine) AllocSubnetLocked() subnet.SubnetAllocator {
	return e.sys.AllocSubnet()
}

func (e *engine) RemoveNode(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	e.mainWorkspace.RemoveNode(id)
}

func (e *engine) RemoveNodeLocked(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	e.mainWorkspace.RemoveNodeLocked(id)
}

func (e *engine) RemoveNodes(workspace string, ids []uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodes(ids)
}

func (e *engine) RemoveNodesLocked(workspace string, ids []uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodesLocked(ids)
}

func (e *engine) RemoveNodesByClass(workspace string, class string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodesByClass(class)
}

func (e *engine) RemoveNodesByClassLocked(workspace string, class string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodesByClassLocked(class)
}

func (e *engine) RemoveLink(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	e.mainWorkspace.RemoveLink(id)
}

func (e *engine) RemoveLinkLocked(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	e.mainWorkspace.RemoveLinkLocked(id)
}

func (e *engine) WorkspaceSnapshot(workspace string) (pasta.WorkspaceSnapshot, bool) {
	if workspace != "main" {
		return pasta.WorkspaceSnapshot{}, false
	}
	return e.mainWorkspace.Snapshot(), true
}

func (e *engine) WorkspaceSnapshotLocked(workspace string) (pasta.WorkspaceSnapshot, bool) {
	if workspace != "main" {
		return pasta.WorkspaceSnapshot{}, false
	}
	return e.mainWorkspace.SnapshotLocked(), true
}

func (e *engine) NodeClassSnapshot(workspace string, name string) (pasta.NodeClassSnapshot, bool) {
	if workspace != "main" {
		return pasta.NodeClassSnapshot{}, false
	}
	return e.mainWorkspace.NodeClassSnapshot(name)
}

func (e *engine) NodeClassSnapshotLocked(workspace string, name string) (pasta.NodeClassSnapshot, bool) {
	if workspace != "main" {
		return pasta.NodeClassSnapshot{}, false
	}
	return e.mainWorkspace.NodeClassSnapshotLocked(name)
}

func (e *engine) NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool) {
	if workspace != "main" {
		return pasta.NodeSnapshot{}, false
	}
	return e.mainWorkspace.NodeSnapshot(id)
}

func (e *engine) NodeSnapshotLocked(workspace string, id uint64) (pasta.NodeSnapshot, bool) {
	if workspace != "main" {
		return pasta.NodeSnapshot{}, false
	}
	return e.mainWorkspace.NodeSnapshotLocked(id)
}

func (e *engine) PortSnapshot(workspace string, id uint64) (pasta.PortSnapshot, bool) {
	if workspace != "main" {
		return pasta.PortSnapshot{}, false
	}
	return e.mainWorkspace.PortSnapshot(id)
}

func (e *engine) PortSnapshotLocked(workspace string, id uint64) (pasta.PortSnapshot, bool) {
	if workspace != "main" {
		return pasta.PortSnapshot{}, false
	}
	return e.mainWorkspace.PortSnapshotLocked(id)
}

func (e *engine) LinkSnapshot(workspace string, id uint64) (pasta.LinkSnapshot, bool) {
	if workspace != "main" {
		return pasta.LinkSnapshot{}, false
	}
	return e.mainWorkspace.LinkSnapshot(id)
}

func (e *engine) LinkSnapshotLocked(workspace string, id uint64) (pasta.LinkSnapshot, bool) {
	if workspace != "main" {
		return pasta.LinkSnapshot{}, false
	}
	return e.mainWorkspace.LinkSnapshotLocked(id)
}

func (e *engine) NodeClassLongDescription(workspace string, name string) (string, bool) {
	if workspace != "main" {
		return "", false
	}
	return e.mainWorkspace.NodeClassLongDescription(name)
}

func (e *engine) NodeClassLongDescriptionLocked(workspace string, name string) (string, bool) {
	if workspace != "main" {
		return "", false
	}
	return e.mainWorkspace.NodeClassLongDescriptionLocked(name)
}

func (e *engine) AddNode(workspace string, class string, name string) (uint64, error) {
	if workspace != "main" {
		return 0, ErrNoWorkspace
	}
	return e.mainWorkspace.AddNodeByClass(class, name)
}

func (e *engine) AddNodeLocked(workspace string, class string, name string) (uint64, error) {
	if workspace != "main" {
		return 0, ErrNoWorkspace
	}
	return e.mainWorkspace.AddNodeByClassLocked(class, name)
}

func (e *engine) ReplaceNodeWithPlaceholder(workspace string, id uint64) error {
	if workspace != "main" {
		return ErrNoWorkspace
	}
	return e.mainWorkspace.ReplaceNodeWithPlaceholder(id, []pasta.Port{})
}

func (e *engine) ReplaceNodeWithPlaceholderLocked(workspace string, id uint64) error {
	if workspace != "main" {
		return ErrNoWorkspace
	}
	return e.mainWorkspace.ReplaceNodeWithPlaceholderLocked(id, []pasta.Port{})
}

func (e *engine) AddLink(workspace string, pa, pb uint64) (uint64, string, error) {
	if workspace != "main" {
		return 0, "", ErrNoWorkspace
	}
	return e.mainWorkspace.AddLink(pa, pb)
}

func (e *engine) AddLinkLocked(workspace string, pa, pb uint64) (uint64, string, error) {
	if workspace != "main" {
		return 0, "", ErrNoWorkspace
	}
	return e.mainWorkspace.AddLinkLocked(pa, pb)
}

func (e *engine) NodeByName(workspace string, name string) (uint64, bool) {
	if workspace != "main" {
		return 0, false
	}
	return e.mainWorkspace.NodeIDByName(name)
}

func (e *engine) NodeByNameLocked(workspace string, name string) (uint64, bool) {
	if workspace != "main" {
		return 0, false
	}
	return e.mainWorkspace.NodeIDByNameLocked(name)
}

func (e *engine) PortsConnected(workspace string, pa, pb uint64) bool {
	if workspace != "main" {
		return false
	}
	return e.mainWorkspace.PortsConnected(pa, pb)
}

func (e *engine) PortsConnectedLocked(workspace string, pa, pb uint64) bool {
	if workspace != "main" {
		return false
	}
	return e.mainWorkspace.PortsConnectedLocked(pa, pb)
}

func (e *engine) NodesConnected(workspace string, na, nb uint64) bool {
	if workspace != "main" {
		return false
	}
	return e.mainWorkspace.NodesConnected(na, nb)
}

func (e *engine) NodesConnectedLocked(workspace string, na, nb uint64) bool {
	if workspace != "main" {
		return false
	}
	return e.mainWorkspace.NodesConnectedLocked(na, nb)
}

func (e *engine) LinkByPorts(workspace string, pa, pb uint64) (uint64, pasta.LinkSnapshot, bool) {
	if workspace != "main" {
		return 0, pasta.LinkSnapshot{}, false
	}
	return e.mainWorkspace.LinkByPorts(pa, pb)
}

func (e *engine) LinkByPortsLocked(workspace string, pa, pb uint64) (uint64, pasta.LinkSnapshot, bool) {
	if workspace != "main" {
		return 0, pasta.LinkSnapshot{}, false
	}
	return e.mainWorkspace.LinkByPortsLocked(pa, pb)
}

func (e *engine) LinksByNodes(workspace string, na, nb uint64) map[uint64]pasta.LinkSnapshot {
	if workspace != "main" {
		return make(map[uint64]pasta.LinkSnapshot)
	}
	return e.mainWorkspace.LinksByNodes(na, nb)
}

func (e *engine) LinksByNodesLocked(workspace string, na, nb uint64) map[uint64]pasta.LinkSnapshot {
	if workspace != "main" {
		return make(map[uint64]pasta.LinkSnapshot)
	}
	return e.mainWorkspace.LinksByNodesLocked(na, nb)
}

func (e *engine) SetNodePosition(workspace string, id uint64, position string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.SetNodePosition(id, position)
}

func (e *engine) SetNodePositionLocked(workspace string, id uint64, position string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.SetNodePositionLocked(id, position)
}

func (e *engine) MoveNodes(workspace string, positions map[uint64]string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.MoveNodes(positions)
}

func (e *engine) MoveNodesLocked(workspace string, positions map[uint64]string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.MoveNodesLocked(positions)
}

func (e *engine) SetNodeName(workspace string, id uint64, name string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.SetNodeName(id, name)
}

func (e *engine) SetNodeNameLocked(workspace string, id uint64, name string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.SetNodeNameLocked(id, name)
}

func (e *engine) RemoveNodePopup(workspace string, id, popupID uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopup(id, popupID)
}

func (e *engine) RemoveNodePopupLocked(workspace string, id, popupID uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopupLocked(id, popupID)
}

func (e *engine) RemoveNodePopups(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopups(id)
}

func (e *engine) RemoveNodePopupsLocked(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopupsLocked(id)
}

func (e *engine) RemoveNodePopupsByType(workspace string, id uint64, popupType string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopupsByType(id, popupType)
}

func (e *engine) RemoveNodePopupsByTypeLocked(workspace string, id uint64, popupType string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopupsByTypeLocked(id, popupType)
}

func (e *engine) Copy(workspace string, ids []uint64) string {
	if workspace != "main" {
		return ""
	}

	data := amnesianodes.SanitizeClipboard(e.mainWorkspace.Copy(ids))
	return sanitizeXTorDriverClipboard(data)
}

func (e *engine) CopyLocked(workspace string, ids []uint64) string {
	if workspace != "main" {
		return ""
	}

	data := amnesianodes.SanitizeClipboard(e.mainWorkspace.CopyLocked(ids))
	return sanitizeXTorDriverClipboard(data)
}

func (e *engine) Paste(workspace string, data string) []uint64 {
	if workspace != "main" {
		return []uint64{}
	}

	return e.mainWorkspace.Paste(data)
}

func (e *engine) PasteLocked(workspace string, data string) []uint64 {
	if workspace != "main" {
		return []uint64{}
	}

	return e.mainWorkspace.PasteLocked(data)
}

func (e *engine) SendInbox(workspace string, message pasta.InboxMessage) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.SendInbox(message)
}

func (e *engine) SendInboxLocked(workspace string, message pasta.InboxMessage) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.SendInboxLocked(message)
}

func (e *engine) Trigger(workspace string, id uint64) error {
	if workspace != "main" {
		return ErrNoWorkspace
	}

	return e.mainWorkspace.Trigger(id)
}

func (e *engine) TriggerLocked(workspace string, id uint64) error {
	if workspace != "main" {
		return ErrNoWorkspace
	}

	return e.mainWorkspace.TriggerLocked(id)
}

func (e *engine) Undo(workspace string) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.Undo()
}

func (e *engine) UndoLocked(workspace string) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.UndoLocked()
}

func (e *engine) Redo(workspace string) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.Redo()
}

func (e *engine) RedoLocked(workspace string) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.RedoLocked()
}

func (e *engine) SaveConfig(cfg configer.Config) error {
	return e.mainWorkspace.SaveConfig(cfg.View(configer.Path{"nodes"}))
}

func (e *engine) SaveConfigLocked(cfg configer.Config) error {
	return e.mainWorkspace.SaveConfigLocked(cfg.View(configer.Path{"nodes"}))
}

func (e *engine) SubscribeNotifications(callback NotificationCallback) uint64 {
	e.Lock()
	defer e.Unlock()
	return e.SubscribeNotificationsLocked(callback)
}

func (e *engine) SubscribeNotificationsLocked(callback NotificationCallback) uint64 {
	if callback == nil {
		return 0
	}

	id := e.nextSubscriptionID
	e.nextSubscriptionID += 1
	e.addNotificationSubscriber(id, callback)

	colormap := make(map[string]TypePalette)
	maps.Copy(colormap, DefaultColorMap)
	e.mu.Unlock()
	callback(EngineNotification{
		SubscriptionID: id,
		Kind:           NotificationColorMap,
		ColorMap:       colormap,
	})

	// TODO: Send engine initial state here (when we'll have some)

	WorkspaceCallback := func(pn pasta.WorkspaceNotification) {
		pn.SubscriptionID = id
		if e.observeWorkspaceBarrier(id, pn) {
			// Full snapshots requested by a publication barrier are internal.
			// The transaction publishes one newer authoritative snapshot.
			return
		}

		en := EngineNotification{
			SubscriptionID: id,
			Kind:           NotificationWorkspace,
			WorkspaceID:    "main",
			Workspace:      &pn,
		}
		e.publishEngineNotification(en)
	}

	wid := e.mainWorkspace.SubscribeNotifications(WorkspaceCallback)
	if wid == 0 {
		e.removeNotificationSubscriber(id)
		e.mu.Lock()
		return 0
	}

	workerCallback := func(worker WorkerNotification) {
		worker.SubscriptionID = id
		e.publishEngineNotification(EngineNotification{
			SubscriptionID: id,
			Kind:           NotificationWorker,
			WorkspaceID:    "main",
			Worker:         &worker,
		})
	}
	workerID := e.workers.subscribe(workerCallback)
	if workerID == 0 {
		_ = e.mainWorkspace.UnsubscribeNotifications(wid)
		e.removeNotificationSubscriber(id)
		e.mu.Lock()
		return 0
	}

	e.mu.Lock()
	e.subscriptionIDMapping[id] = subMap{
		MainWorkspace: wid,
		Workers:       workerID,
	}

	return id
}

func (e *engine) UnsubscribeNotifications(id uint64) {
	subMap, ok := e.takeSubscriptionMap(id)
	if !ok {
		return
	}
	e.removeNotificationSubscriber(id)

	// TODO: Unsubscribe all workspaces here (when we'll have more than one)
	_ = e.mainWorkspace.UnsubscribeNotifications(subMap.MainWorkspace)
	e.workers.unsubscribe(subMap.Workers)
}

func (e *engine) UnsubscribeNotificationsLocked(id uint64) {
	subMap, ok := e.takeSubscriptionMapLocked(id)
	if !ok {
		return
	}
	e.removeNotificationSubscriber(id)

	// TODO: Unsubscribe all workspaces here (when we'll have more than one)
	_ = e.mainWorkspace.UnsubscribeNotificationsLocked(subMap.MainWorkspace)
	e.workers.unsubscribe(subMap.Workers)
}

func (e *engine) RequestFullSnapshot(id uint64) {
	subMap, ok := e.subscriptionMap(id)
	if !ok {
		return
	}

	// TODO: Send engine state here (when we'll have some)
	// TODO: Unsubscribe all workspaces here (when we'll have more than one)
	_ = e.mainWorkspace.RequestFullSnapshot(subMap.MainWorkspace)
	e.workers.requestFullSnapshot(subMap.Workers)
}

func (e *engine) RequestFullSnapshotLocked(id uint64) {
	subMap, ok := e.subscriptionMapLocked(id)
	if !ok {
		return
	}

	// TODO: Send engine state here (when we'll have some)
	// TODO: Unsubscribe all workspaces here (when we'll have more than one)
	_ = e.mainWorkspace.RequestFullSnapshotLocked(subMap.MainWorkspace)
	e.workers.requestFullSnapshot(subMap.Workers)
}

func (e *engine) SubscribeNodeMenu(workspace string, node, id uint64) {
	if workspace != "main" {
		return
	}

	subMap, ok := e.subscriptionMap(id)
	if !ok {
		return
	}

	e.mainWorkspace.SubscribeNodeMenu(node, subMap.MainWorkspace)
}

func (e *engine) SubscribeNodeMenuLocked(workspace string, node, id uint64) {
	if workspace != "main" {
		return
	}

	subMap, ok := e.subscriptionMapLocked(id)
	if !ok {
		return
	}

	e.mainWorkspace.SubscribeNodeMenuLocked(node, subMap.MainWorkspace)
}

func (e *engine) UnsubscribeNodeMenu(workspace string, node, id uint64) {
	if workspace != "main" {
		return
	}

	subMap, ok := e.subscriptionMap(id)
	if !ok {
		return
	}

	e.mainWorkspace.UnsubscribeNodeMenu(node, subMap.MainWorkspace)
}

func (e *engine) UnsubscribeNodeMenuLocked(workspace string, node, id uint64) {
	if workspace != "main" {
		return
	}

	subMap, ok := e.subscriptionMapLocked(id)
	if !ok {
		return
	}

	e.mainWorkspace.UnsubscribeNodeMenuLocked(node, subMap.MainWorkspace)
}

func (e *engine) subscriptionMap(id uint64) (subMap, bool) {
	e.Lock()
	defer e.Unlock()
	return e.subscriptionMapLocked(id)
}

func (e *engine) subscriptionMapLocked(id uint64) (subMap, bool) {
	mapping, ok := e.subscriptionIDMapping[id]
	return mapping, ok
}

func (e *engine) takeSubscriptionMap(id uint64) (subMap, bool) {
	e.Lock()
	defer e.Unlock()
	return e.takeSubscriptionMapLocked(id)
}

func (e *engine) takeSubscriptionMapLocked(id uint64) (subMap, bool) {
	mapping, ok := e.subscriptionIDMapping[id]
	if ok {
		delete(e.subscriptionIDMapping, id)
	}
	return mapping, ok
}

func (e *engine) SendNodeFormularMsg(workspace string, node uint64, message any) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.SendNodeFormularMsg(node, message)
}

func (e *engine) SendNodeFormularMsgLocked(workspace string, node uint64, message any) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.SendNodeFormularMsgLocked(node, message)
}

func (e *engine) WorkersSnapshot(workspace string) (map[uint64]WorkerSnapshot, bool) {
	if workspace != "main" {
		return nil, false
	}
	if e.workers == nil {
		return map[uint64]WorkerSnapshot{}, true
	}
	return e.workers.snapshot(), true
}

func (e *engine) WorkersSnapshotLocked(workspace string) (map[uint64]WorkerSnapshot, bool) {
	return e.WorkersSnapshot(workspace)
}

func (e *engine) SubscribeWorkerNotifications(callback WorkerNotificationCallback) uint64 {
	if e.workers == nil {
		return 0
	}
	return e.workers.subscribe(callback)
}

func (e *engine) SubscribeWorkerNotificationsLocked(callback WorkerNotificationCallback) uint64 {
	return e.SubscribeWorkerNotifications(callback)
}

func (e *engine) UnsubscribeWorkerNotifications(id uint64) {
	if e.workers == nil {
		return
	}
	e.workers.unsubscribe(id)
}

func (e *engine) UnsubscribeWorkerNotificationsLocked(id uint64) {
	e.UnsubscribeWorkerNotifications(id)
}

func (e *engine) RequestWorkerSnapshot(id uint64) {
	if e.workers == nil {
		return
	}
	e.workers.requestFullSnapshot(id)
}

func (e *engine) RequestWorkerSnapshotLocked(id uint64) {
	e.RequestWorkerSnapshot(id)
}

func (e *engine) handleWorkerFailure(worker WorkerSnapshot) {
	if e == nil || e.mainWorkspace == nil || worker.Node == 0 || worker.Orphan {
		return
	}
	if err := e.mainWorkspace.ReplaceNodeWithPlaceholder(worker.Node, []pasta.Port{}); err != nil {
		if e.logger != nil {
			e.logger.Errf("failed to replace worker node with placeholder worker=%d node=%d: %v", worker.ID, worker.Node, err)
		}
		return
	}
	if _, err := e.mainWorkspace.AddNodePopup(worker.Node, pasta.NodePopupErr, workerFailureText(worker), false); err != nil && e.logger != nil {
		e.logger.Errf("failed to add worker failure popup worker=%d node=%d: %v", worker.ID, worker.Node, err)
	}
}
