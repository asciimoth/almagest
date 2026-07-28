package api

import (
	"cmp"
	"encoding/json"
	"slices"
	"strings"

	"github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/pasta/pasta"
)

type EngineMethod string

const (
	MethodEngineSubscribeNotifications   EngineMethod = "subscribe_notifications"
	MethodEngineUnsubscribeNotifications EngineMethod = "unsubscribe_notifications"
	MethodEngineRequestFullSnapshot      EngineMethod = "request_full_snapshot"
	MethodEngineSubscribeNodeMenu        EngineMethod = "subscribe_node_menu"
	MethodEngineUnsubscribeNodeMenu      EngineMethod = "unsubscribe_node_menu"

	MethodEngineWorkspaceSnapshot        EngineMethod = "workspace_snapshot"
	MethodEngineWorkersSnapshot          EngineMethod = "workers_snapshot"
	MethodEngineNodeClassSnapshot        EngineMethod = "node_class_snapshot"
	MethodEngineNodeSnapshot             EngineMethod = "node_snapshot"
	MethodEnginePortSnapshot             EngineMethod = "port_snapshot"
	MethodEngineLinkSnapshot             EngineMethod = "link_snapshot"
	MethodEngineNodeClassLongDescription EngineMethod = "node_class_long_description"

	MethodEngineAddNode                    EngineMethod = "add_node"
	MethodEngineReplaceNodeWithPlaceholder EngineMethod = "replace_node_with_placeholder"
	MethodEngineAddLink                    EngineMethod = "add_link"
	MethodEngineRemoveNode                 EngineMethod = "remove_node"
	MethodEngineRemoveNodes                EngineMethod = "remove_nodes"
	MethodEngineRemoveNodesByClass         EngineMethod = "remove_nodes_by_class"
	MethodEngineRemoveLink                 EngineMethod = "remove_link"
	MethodEngineSetNodePosition            EngineMethod = "set_node_position"
	MethodEngineMoveNodes                  EngineMethod = "move_nodes"
	MethodEngineSetNodeName                EngineMethod = "set_node_name"
	MethodEngineRemoveNodePopup            EngineMethod = "remove_node_popup"
	MethodEngineRemoveNodePopups           EngineMethod = "remove_node_popups"
	MethodEngineRemoveNodePopupsByType     EngineMethod = "remove_node_popups_by_type"
	MethodEngineCopy                       EngineMethod = "copy"
	MethodEnginePaste                      EngineMethod = "paste"
	MethodEngineSendInbox                  EngineMethod = "send_inbox"
	MethodEngineTriggerNode                EngineMethod = "trigger_node"
	MethodEngineUndo                       EngineMethod = "undo"
	MethodEngineRedo                       EngineMethod = "redo"
	MethodEngineSendNodeFormularMessage    EngineMethod = "send_node_formular_msg"

	EventEngineNotification EngineMethod = "notification"
)

func EngineMethods() []string {
	return []string{
		string(MethodEngineSubscribeNotifications),
		string(MethodEngineUnsubscribeNotifications),
		string(MethodEngineRequestFullSnapshot),
		string(MethodEngineSubscribeNodeMenu),
		string(MethodEngineUnsubscribeNodeMenu),
		string(MethodEngineWorkspaceSnapshot),
		string(MethodEngineWorkersSnapshot),
		string(MethodEngineNodeClassSnapshot),
		string(MethodEngineNodeSnapshot),
		string(MethodEnginePortSnapshot),
		string(MethodEngineLinkSnapshot),
		string(MethodEngineNodeClassLongDescription),
		string(MethodEngineAddNode),
		string(MethodEngineReplaceNodeWithPlaceholder),
		string(MethodEngineAddLink),
		string(MethodEngineRemoveNode),
		string(MethodEngineRemoveNodes),
		string(MethodEngineRemoveNodesByClass),
		string(MethodEngineRemoveLink),
		string(MethodEngineSetNodePosition),
		string(MethodEngineMoveNodes),
		string(MethodEngineSetNodeName),
		string(MethodEngineRemoveNodePopup),
		string(MethodEngineRemoveNodePopups),
		string(MethodEngineRemoveNodePopupsByType),
		string(MethodEngineCopy),
		string(MethodEnginePaste),
		string(MethodEngineSendInbox),
		string(MethodEngineTriggerNode),
		string(MethodEngineUndo),
		string(MethodEngineRedo),
		string(MethodEngineSendNodeFormularMessage),
	}
}

func IsEngineMutation(method string) bool {
	switch EngineMethod(method) {
	case MethodEngineAddNode,
		MethodEngineReplaceNodeWithPlaceholder,
		MethodEngineAddLink,
		MethodEngineRemoveNode,
		MethodEngineRemoveNodes,
		MethodEngineRemoveNodesByClass,
		MethodEngineRemoveLink,
		MethodEngineSetNodePosition,
		MethodEngineMoveNodes,
		MethodEngineSetNodeName,
		MethodEngineRemoveNodePopup,
		MethodEngineRemoveNodePopups,
		MethodEngineRemoveNodePopupsByType,
		MethodEnginePaste,
		MethodEngineSendInbox,
		MethodEngineTriggerNode,
		MethodEngineUndo,
		MethodEngineRedo,
		MethodEngineSendNodeFormularMessage:
		return true
	default:
		return false
	}
}

type WorkspaceRequest struct {
	Workspace string `json:"workspace"`
}

type IDRequest struct {
	Workspace string `json:"workspace"`
	ID        uint64 `json:"id"`
}

// IDsRequest identifies a batch of nodes in a workspace.
//
// Batch node mutations ignore missing, duplicate, and invalid IDs to match
// pasta.Workspace batch behavior.
type IDsRequest struct {
	Workspace string   `json:"workspace"`
	IDs       []uint64 `json:"ids"`
}

type ClassRequest struct {
	Workspace string `json:"workspace"`
	Class     string `json:"class"`
}

type NameRequest struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

type AddNodeRequest struct {
	Workspace string `json:"workspace"`
	Class     string `json:"class"`
	Name      string `json:"name,omitempty"`
}

type AddNodeResponse struct {
	ID uint64 `json:"id"`
}

type AddLinkRequest struct {
	Workspace string `json:"workspace"`
	PortA     uint64 `json:"port_a"`
	PortB     uint64 `json:"port_b"`
}

type AddLinkResponse struct {
	ID   uint64 `json:"id"`
	Type string `json:"type"`
}

type SetNodePositionRequest struct {
	Workspace string `json:"workspace"`
	ID        uint64 `json:"id"`
	Position  string `json:"position"`
}

// MoveNodesRequest sets node position strings for multiple nodes in one
// workspace mutation. Positions are opaque to the daemon and interpreted by the
// panel frontend.
type MoveNodesRequest struct {
	Workspace string            `json:"workspace"`
	Positions map[uint64]string `json:"positions"`
}

type SetNodeNameRequest struct {
	Workspace string `json:"workspace"`
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
}

type NodePopupRequest struct {
	Workspace string `json:"workspace"`
	Node      uint64 `json:"node"`
	Popup     uint64 `json:"popup,omitempty"`
	Type      string `json:"type,omitempty"`
}

type CopyRequest struct {
	Workspace string   `json:"workspace"`
	IDs       []uint64 `json:"ids"`
}

type CopyResponse struct {
	Data string `json:"data"`
}

type PasteRequest struct {
	Workspace string `json:"workspace"`
	Data      string `json:"data"`
}

type PasteResponse struct {
	IDs []uint64 `json:"ids"`
}

type SubscribeNotificationsResponse struct {
	SubscriptionID uint64 `json:"subscription_id"`
}

type SubscriptionRequest struct {
	SubscriptionID uint64 `json:"subscription_id"`
}

type NodeMenuSubscriptionRequest struct {
	Workspace      string `json:"workspace"`
	Node           uint64 `json:"node"`
	SubscriptionID uint64 `json:"subscription_id"`
}

type EngineNotificationEvent struct {
	SubscriptionID uint64             `json:"subscription_id"`
	Notification   EngineNotification `json:"notification"`
}

// EngineNotification is the daemon API representation of an engine
// notification. It mirrors lib.EngineNotification but uses API snapshot DTOs so
// clients receive the same ordered class list from direct responses and events.
type EngineNotification struct {
	Kind almagest.NotificationKind `json:"kind"`

	// WorkspaceID is an ID of workspace sent notification.
	// Currently there is only "main" one.
	WorkspaceID string                       `json:"workspace_id,omitempty"`
	Workspace   *WorkspaceNotification       `json:"workspace_notification,omitempty"`
	Worker      *almagest.WorkerNotification `json:"worker_notification,omitempty"`

	ColorMap map[string]almagest.TypePalette `json:"colormap,omitempty"`
}

// NewEngineNotification converts a library engine notification into the public
// API shape used on the wire.
func NewEngineNotification(notification almagest.EngineNotification) EngineNotification {
	out := EngineNotification{
		Kind:        notification.Kind,
		WorkspaceID: notification.WorkspaceID,
		Worker:      notification.Worker,
		ColorMap:    notification.ColorMap,
	}
	if notification.Workspace != nil {
		workspace := NewWorkspaceNotification(*notification.Workspace)
		out.Workspace = &workspace
	}
	return out
}

// WorkspaceNotification is the API representation of a Pasta workspace
// notification. Full snapshots use WorkspaceSnapshot so class arrays are
// ordered before the notification is serialized for clients.
type WorkspaceNotification struct {
	Kind pasta.NotificationKind `json:"kind"`
	ID   uint64                 `json:"id,omitempty"`

	ClassName string `json:"class_name,omitempty"`

	Snapshot  *WorkspaceSnapshot            `json:"snapshot,omitempty"`
	NodeClass *pasta.NodeClassSnapshot      `json:"node_class,omitempty"`
	Node      *pasta.NodeSnapshot           `json:"node,omitempty"`
	Nodes     map[uint64]pasta.NodeSnapshot `json:"nodes,omitempty"`
	Port      *pasta.PortSnapshot           `json:"port,omitempty"`
	Link      *pasta.LinkSnapshot           `json:"link,omitempty"`
	Formular  any                           `json:"formular,omitempty"`
}

// NewWorkspaceNotification converts a Pasta workspace notification into the
// API shape, preserving single-entity notification payloads and sorting any
// embedded full workspace snapshot.
func NewWorkspaceNotification(notification pasta.WorkspaceNotification) WorkspaceNotification {
	out := WorkspaceNotification{
		Kind:      notification.Kind,
		ID:        notification.ID,
		ClassName: notification.ClassName,
		NodeClass: notification.NodeClass,
		Node:      notification.Node,
		Nodes:     notification.Nodes,
		Port:      notification.Port,
		Link:      notification.Link,
		Formular:  notification.Formular,
	}
	if notification.Snapshot != nil {
		snapshot := NewWorkspaceSnapshot(*notification.Snapshot)
		out.Snapshot = &snapshot
	}
	return out
}

type SendInboxRequest struct {
	Workspace string          `json:"workspace"`
	Node      uint64          `json:"node"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

func (r SendInboxRequest) InboxMessage() pasta.InboxMessage {
	return pasta.InboxMessage{
		ReceiverNode: r.Node,
		Payload:      r.Payload,
	}
}

type SendNodeFormularMessageRequest struct {
	Workspace string          `json:"workspace"`
	Node      uint64          `json:"node"`
	Message   json.RawMessage `json:"message,omitempty"`
}

type WorkspaceSnapshotResponse struct {
	Snapshot WorkspaceSnapshot `json:"snapshot"`
}

// WorkersSnapshotResponse is the focused worker-system view returned by
// workers_snapshot. Nodes contains only live parent nodes referenced by Workers;
// missing worker parents are intentionally omitted so clients can render those
// workers as attached to gone nodes using WorkerSnapshot.Orphan.
type WorkersSnapshotResponse struct {
	Nodes   map[uint64]pasta.NodeSnapshot      `json:"nodes"`
	Workers map[uint64]almagest.WorkerSnapshot `json:"workers"`
}

type NodeClassSnapshotResponse struct {
	Snapshot pasta.NodeClassSnapshot `json:"snapshot"`
}

type NodeSnapshotResponse struct {
	Snapshot pasta.NodeSnapshot `json:"snapshot"`
}

type PortSnapshotResponse struct {
	Snapshot pasta.PortSnapshot `json:"snapshot"`
}

type LinkSnapshotResponse struct {
	Snapshot pasta.LinkSnapshot `json:"snapshot"`
}

type NodeClassLongDescriptionResponse struct {
	Description string `json:"description"`
}

// WorkspaceSnapshot is the daemon API representation of a Pasta workspace
// snapshot. Classes is a sorted list rather than Pasta's internal name-keyed
// map so clients can render node class palettes in a stable server-defined
// order.
type WorkspaceSnapshot struct {
	Classes []pasta.NodeClassSnapshot     `json:"classes"`
	Nodes   map[uint64]pasta.NodeSnapshot `json:"nodes"`
	Ports   map[uint64]pasta.PortSnapshot `json:"ports"`
	Links   map[uint64]pasta.LinkSnapshot `json:"links"`
}

// NewWorkspaceSnapshot converts a Pasta workspace snapshot into the public API
// snapshot shape, sorting node classes at the daemon boundary.
func NewWorkspaceSnapshot(snapshot pasta.WorkspaceSnapshot) WorkspaceSnapshot {
	return WorkspaceSnapshot{
		Classes: SortedNodeClassSnapshots(snapshot.Classes),
		Nodes:   snapshot.Nodes,
		Ports:   snapshot.Ports,
		Links:   snapshot.Links,
	}
}

// SortedNodeClassSnapshots returns class snapshots in the canonical API order:
// almagest namespace first, pasta second, other namespaces alphabetically, then
// typed classes by primary type, untyped classes, and finally class name.
func SortedNodeClassSnapshots(classes map[string]pasta.NodeClassSnapshot) []pasta.NodeClassSnapshot {
	snapshots := make([]pasta.NodeClassSnapshot, 0, len(classes))
	for name, class := range classes {
		if class.Class == "" {
			class.Class = name
		}
		snapshots = append(snapshots, class)
	}
	slices.SortFunc(snapshots, CompareNodeClassSnapshots)
	return snapshots
}

// CompareNodeClassSnapshots compares node class snapshots using the canonical
// daemon API order documented by SortedNodeClassSnapshots.
func CompareNodeClassSnapshots(a, b pasta.NodeClassSnapshot) int {
	if c := cmp.Compare(nodeClassNamespaceRank(a.Class), nodeClassNamespaceRank(b.Class)); c != 0 {
		return c
	}
	if c := cmp.Compare(nodeClassNamespace(a.Class), nodeClassNamespace(b.Class)); c != 0 {
		return c
	}
	if c := cmp.Compare(nodeClassPrimaryRank(a.PrimaryType), nodeClassPrimaryRank(b.PrimaryType)); c != 0 {
		return c
	}
	if c := cmp.Compare(a.PrimaryType, b.PrimaryType); c != 0 {
		return c
	}
	return cmp.Compare(a.Class, b.Class)
}

func nodeClassNamespace(class string) string {
	namespace, _, ok := strings.Cut(class, "/")
	if !ok {
		return class
	}
	return namespace
}

func nodeClassNamespaceRank(class string) int {
	switch nodeClassNamespace(class) {
	case "almagest":
		return 0
	case "pasta":
		return 1
	default:
		return 2
	}
}

func nodeClassPrimaryRank(primaryType string) int {
	if primaryType == "" {
		return 1
	}
	return 0
}
