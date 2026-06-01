package lib

import (
	"context"
	"errors"
	"io"
	"maps"

	"github.com/asciimoth/bufpool"
	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/gonnect/sysnet"
	"github.com/asciimoth/pasta/pasta"
)

var (
	// Static type assertion
	_ Engine = &engine{}
)

var (
	ErrNoWorkspace = errors.New("no such workspace")
)

type NotificationKind string

const (
	// NotificationWorkspace carries a wrapped workspace notification
	NotificationWorkspace NotificationKind = "workspace"
	NotificationColorMap  NotificationKind = "colormap"
)

// EngineNotification describes one observable engine state change.
type EngineNotification struct {
	SubscriptionID uint64 `json:"-"`

	Kind NotificationKind `json:"kind"`

	// WorkspaceID is an ID of workspace sent notification.
	// Currently there is only "main" one.
	WorkspaceID string                       `json:"workspace_id,omitempty"`
	Workspace   *pasta.WorkspaceNotification `json:"workspace_notification,omitempty"`

	ColorMap map[string]TypePalette `json:"colormap,omitempty"`
}

type NotificationCallback func(EngineNotification)

func BuildEngine(
	ctx context.Context,
	cfg configer.Config, // may be nil
	logf LogFactory,
	sys sysnet.SysBuilder, // use sysnet.NewDebugSysBuilder() if you dont have one
	extra []pasta.NodeClass,
	pool bufpool.Pool, // may be nil. Use bufpool.NewTestDebugPool() in tests
) (Engine, error) {
	ctx, cancel := context.WithCancel(ctx)

	engine := &engine{
		ctx:                   ctx,
		ctxCancel:             cancel,
		logf:                  logf,
		logger:                logf.EngineLogger(),
		pool:                  pool,
		sys:                   sys,
		mainWorkspace:         nil,
		nextSubscriptionID:    100, // to avoid collision between our IDs and workspace ones
		subscriptionIDMapping: make(map[uint64]subMap),
	}

	classes := NodeClasses(engine)
	classes = append(classes, extra...)

	work, err := pasta.WorkspaceFromConfig(
		classes,
		cfg.View(configer.Path{"nodes"}),
		logf,
	)

	if err != nil {
		return nil, err
	}

	engine.mainWorkspace = work

	return engine, nil
}

// pasta.Workspace methods re-exported by Engine
type PastaAPI interface {
	RemoveNode(workspace string, id uint64)
	// RemoveNodesByClass removes all nodes with class.
	RemoveNodesByClass(workspace string, class string)
	// RemoveLink removes a link by it's id
	RemoveLink(workspace string, id uint64)

	WorkspaceSnapshot(workspace string) (pasta.WorkspaceSnapshot, bool)
	NodeClassSnapshot(workspace string, name string) (pasta.NodeClassSnapshot, bool)
	NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool)
	PortSnapshot(workspace string, id uint64) (pasta.PortSnapshot, bool)
	LinkSnapshot(workspace string, id uint64) (pasta.LinkSnapshot, bool)

	NodeClassLongDescription(workspace string, name string) (string, bool)

	// Add node by it's class name
	AddNode(workspace string, class string, name string) (uint64, error)
	// ReplaceNodeWithPlaceholder replaces an existing node implementation with a
	// placeholder record while preserving existing ports and links.
	ReplaceNodeWithPlaceholder(workspace string, id uint64) error
	// AddLink connects two ports and returns the new link ID and selected link type.
	AddLink(workspace string, pa, pb uint64) (uint64, string, error)

	// NodeIDByName returns the workspace ID for a node name.
	NodeByName(workspace string, name string) (uint64, bool)

	// PortsConnected reports whether two ports are connected by a link.
	PortsConnected(workspace string, pa, pb uint64) bool
	// NodesConnected reports whether two nodes have at least one direct link.
	NodesConnected(workspace string, na, nb uint64) bool
	// LinkByPorts returns the link connecting two ports.
	LinkByPorts(workspace string, pa, pb uint64) (uint64, pasta.LinkSnapshot, bool)
	LinksByNodes(workspace string, na, nb uint64) map[uint64]pasta.LinkSnapshot

	SetNodePosition(workspace string, id uint64, position string)
	SetNodeName(workspace string, id uint64, name string)

	RemoveNodePopup(workspace string, id, popupID uint64)
	RemoveNodePopups(workspace string, id uint64)
	RemoveNodePopupsByType(workspace string, id uint64, popupType string)

	// Copy serializes the selected nodes and links between them into an opaque
	// clipboard string.
	// Missing node IDs and duplicate selections are ignored. Links are included
	// only when both endpoint nodes are selected. If any selected live node fails
	// OnSave, Copy returns an empty string.
	Copy(workspace string, ids []uint64) string
	// Paste restores a clipboard string as new nodes and returns their new IDs.
	// Invalid clipboard data and individual node/link restore failures are ignored.
	// Available class factories may recreate live nodes; otherwise placeholders are
	// created so copied graph structure can still be restored.
	Paste(workspace string, data string) []uint64

	// SendInbox schedules delivery of message to its receiver node.
	SendInbox(workspace string, message pasta.InboxMessage)

	Undo(workspace string)
	Redo(workspace string)
}

type Engine interface {
	io.Closer
	PastaAPI

	// It should be save to call Lock recursive.
	// In other words,  Engine should provide a recursive mutex implementation.
	Lock()
	Unlock()

	// Saves internal pasta.Workspace config under `nodes` key
	SaveConfig(cfg configer.Config) error

	// SubscribeNotifications subscribes callback to engine notifications.
	// The callback receives a full workspace snapshot before SubscribeNotifications
	// returns. The returned subscription ID can be passed to
	// UnsubscribeNotifications.
	SubscribeNotifications(callback NotificationCallback) uint64
	UnsubscribeNotifications(id uint64)
	RequestFullSnapshot(subscriptionID uint64)

	// Currently only "main" workspace available
	SubscribeNodeMenu(workspace string, node, subscriptionID uint64)
	UnsubscribeNodeMenu(workspace string, node, subscriptionID uint64)

	SendNodeFormularMsg(workspace string, node uint64, message any)
}

type subMap struct {
	MainWorkspace uint64
}

type engine struct {
	ctx       context.Context
	ctxCancel context.CancelFunc

	logf   LogFactory
	logger Logger

	pool bufpool.Pool

	sys sysnet.SysBuilder

	mainWorkspace *pasta.Workspace

	nextSubscriptionID uint64
	// mapping from engine notification subscription IDs to internal
	subscriptionIDMapping map[uint64]subMap
}

func (e *engine) Lock() {
	e.mainWorkspace.Lock()
}

func (e *engine) Unlock() {
	e.mainWorkspace.Unlock()
}

func (e *engine) Close() error {
	e.ctxCancel()
	e.mainWorkspace.Close()
	// TODO: Close other workspaces
	return e.sys.Close()
}

func (e *engine) RemoveNode(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	e.mainWorkspace.RemoveNode(id)
}

func (e *engine) RemoveNodesByClass(workspace string, class string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodesByClass(class)
}

func (e *engine) RemoveLink(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	e.mainWorkspace.RemoveLink(id)
}

func (e *engine) WorkspaceSnapshot(workspace string) (pasta.WorkspaceSnapshot, bool) {
	if workspace != "main" {
		return pasta.WorkspaceSnapshot{}, false
	}
	return e.mainWorkspace.Snapshot(), true
}

func (e *engine) NodeClassSnapshot(workspace string, name string) (pasta.NodeClassSnapshot, bool) {
	if workspace != "main" {
		return pasta.NodeClassSnapshot{}, false
	}
	return e.mainWorkspace.NodeClassSnapshot(name)
}

func (e *engine) NodeSnapshot(workspace string, id uint64) (pasta.NodeSnapshot, bool) {
	if workspace != "main" {
		return pasta.NodeSnapshot{}, false
	}
	return e.mainWorkspace.NodeSnapshot(id)
}

func (e *engine) PortSnapshot(workspace string, id uint64) (pasta.PortSnapshot, bool) {
	if workspace != "main" {
		return pasta.PortSnapshot{}, false
	}
	return e.mainWorkspace.PortSnapshot(id)
}

func (e *engine) LinkSnapshot(workspace string, id uint64) (pasta.LinkSnapshot, bool) {
	if workspace != "main" {
		return pasta.LinkSnapshot{}, false
	}
	return e.mainWorkspace.LinkSnapshot(id)
}

func (e *engine) NodeClassLongDescription(workspace string, name string) (string, bool) {
	if workspace != "main" {
		return "", false
	}
	return e.mainWorkspace.NodeClassLongDescription(name)
}

func (e *engine) AddNode(workspace string, class string, name string) (uint64, error) {
	if workspace != "main" {
		return 0, ErrNoWorkspace
	}
	return e.mainWorkspace.AddNodeByClass(class, name)
}

func (e *engine) ReplaceNodeWithPlaceholder(workspace string, id uint64) error {
	if workspace != "main" {
		return ErrNoWorkspace
	}
	return e.mainWorkspace.ReplaceNodeWithPlaceholder(id, []pasta.Port{})
}

func (e *engine) AddLink(workspace string, pa, pb uint64) (uint64, string, error) {
	if workspace != "main" {
		return 0, "", ErrNoWorkspace
	}
	return e.mainWorkspace.AddLink(pa, pb)
}

func (e *engine) NodeByName(workspace string, name string) (uint64, bool) {
	if workspace != "main" {
		return 0, false
	}
	return e.mainWorkspace.NodeIDByName(name)
}

func (e *engine) PortsConnected(workspace string, pa, pb uint64) bool {
	if workspace != "main" {
		return false
	}
	return e.mainWorkspace.PortsConnected(pa, pb)
}

func (e *engine) NodesConnected(workspace string, na, nb uint64) bool {
	if workspace != "main" {
		return false
	}
	return e.mainWorkspace.NodesConnected(na, nb)
}

func (e *engine) LinkByPorts(workspace string, pa, pb uint64) (uint64, pasta.LinkSnapshot, bool) {
	if workspace != "main" {
		return 0, pasta.LinkSnapshot{}, false
	}
	return e.mainWorkspace.LinkByPorts(pa, pb)
}

func (e *engine) LinksByNodes(workspace string, na, nb uint64) map[uint64]pasta.LinkSnapshot {
	if workspace != "main" {
		return make(map[uint64]pasta.LinkSnapshot)
	}
	return e.mainWorkspace.LinksByNodes(na, nb)
}

func (e *engine) SetNodePosition(workspace string, id uint64, position string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.SetNodePosition(id, position)
}

func (e *engine) SetNodeName(workspace string, id uint64, name string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.SetNodeName(id, name)
}

func (e *engine) RemoveNodePopup(workspace string, id, popupID uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopup(id, popupID)
}

func (e *engine) RemoveNodePopups(workspace string, id uint64) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopups(id)
}

func (e *engine) RemoveNodePopupsByType(workspace string, id uint64, popupType string) {
	if workspace != "main" {
		return
	}
	_ = e.mainWorkspace.RemoveNodePopupsByType(id, popupType)
}

func (e *engine) Copy(workspace string, ids []uint64) string {
	if workspace != "main" {
		return ""
	}

	return e.mainWorkspace.Copy(ids)
}

func (e *engine) Paste(workspace string, data string) []uint64 {
	if workspace != "main" {
		return []uint64{}
	}

	return e.mainWorkspace.Paste(data)
}

func (e *engine) SendInbox(workspace string, message pasta.InboxMessage) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.SendInbox(message)
}

func (e *engine) Undo(workspace string) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.Undo()
}

func (e *engine) Redo(workspace string) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.Redo()
}

func (e *engine) SaveConfig(cfg configer.Config) error {
	return e.mainWorkspace.SaveConfig(cfg.View(configer.Path{"nodes"}))
}

func (e *engine) SubscribeNotifications(callback NotificationCallback) uint64 {
	e.Lock()
	defer e.Unlock()

	id := e.nextSubscriptionID
	e.nextSubscriptionID += 1

	colormap := make(map[string]TypePalette)
	maps.Copy(colormap, DefaultColorMap)
	callback(EngineNotification{
		SubscriptionID: id,
		Kind:           NotificationColorMap,
		ColorMap:       colormap,
	})

	// TODO: Send engine initial state here (when we'll have some)

	WorkspaceCallback := func(pn pasta.WorkspaceNotification) {
		pn.SubscriptionID = id

		en := EngineNotification{
			SubscriptionID: id,
			Kind:           NotificationWorkspace,
			WorkspaceID:    "main",
			Workspace:      &pn,
		}
		callback(en)
	}

	wid := e.mainWorkspace.SubscribeNotifications(WorkspaceCallback)
	if wid == 0 {
		return 0
	}

	e.subscriptionIDMapping[id] = subMap{
		MainWorkspace: wid,
	}

	return id
}

func (e *engine) UnsubscribeNotifications(id uint64) {
	subMap, ok := e.subscriptionIDMapping[id]
	if !ok {
		return
	}

	// TODO: Unsubscribe all workspaces here (when we'll have more than one)
	_ = e.mainWorkspace.UnsubscribeNotifications(subMap.MainWorkspace)
}

func (e *engine) RequestFullSnapshot(id uint64) {
	subMap, ok := e.subscriptionIDMapping[id]
	if !ok {
		return
	}

	// TODO: Send engine state here (when we'll have some)

	// TODO: Unsubscribe all workspaces here (when we'll have more than one)
	_ = e.mainWorkspace.RequestFullSnapshot(subMap.MainWorkspace)
}

func (e *engine) SubscribeNodeMenu(workspace string, node, id uint64) {
	if workspace != "main" {
		return
	}

	subMap, ok := e.subscriptionIDMapping[id]
	if !ok {
		return
	}

	e.mainWorkspace.SubscribeNodeMenu(node, subMap.MainWorkspace)
}

func (e *engine) UnsubscribeNodeMenu(workspace string, node, id uint64) {
	if workspace != "main" {
		return
	}

	subMap, ok := e.subscriptionIDMapping[id]
	if !ok {
		return
	}

	e.mainWorkspace.UnsubscribeNodeMenu(node, subMap.MainWorkspace)
}

func (e *engine) SendNodeFormularMsg(workspace string, node uint64, message any) {
	if workspace != "main" {
		return
	}

	e.mainWorkspace.SendNodeFormularMsg(node, message)
}
