package api

import (
	"encoding/json"

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
	MethodEngineNodeClassSnapshot        EngineMethod = "node_class_snapshot"
	MethodEngineNodeSnapshot             EngineMethod = "node_snapshot"
	MethodEnginePortSnapshot             EngineMethod = "port_snapshot"
	MethodEngineLinkSnapshot             EngineMethod = "link_snapshot"
	MethodEngineNodeClassLongDescription EngineMethod = "node_class_long_description"

	MethodEngineAddNode                    EngineMethod = "add_node"
	MethodEngineReplaceNodeWithPlaceholder EngineMethod = "replace_node_with_placeholder"
	MethodEngineAddLink                    EngineMethod = "add_link"
	MethodEngineRemoveNode                 EngineMethod = "remove_node"
	MethodEngineRemoveNodesByClass         EngineMethod = "remove_nodes_by_class"
	MethodEngineRemoveLink                 EngineMethod = "remove_link"
	MethodEngineSetNodePosition            EngineMethod = "set_node_position"
	MethodEngineSetNodeName                EngineMethod = "set_node_name"
	MethodEngineRemoveNodePopup            EngineMethod = "remove_node_popup"
	MethodEngineRemoveNodePopups           EngineMethod = "remove_node_popups"
	MethodEngineRemoveNodePopupsByType     EngineMethod = "remove_node_popups_by_type"
	MethodEngineCopy                       EngineMethod = "copy"
	MethodEnginePaste                      EngineMethod = "paste"
	MethodEngineSendInbox                  EngineMethod = "send_inbox"
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
		string(MethodEngineNodeClassSnapshot),
		string(MethodEngineNodeSnapshot),
		string(MethodEnginePortSnapshot),
		string(MethodEngineLinkSnapshot),
		string(MethodEngineNodeClassLongDescription),
		string(MethodEngineAddNode),
		string(MethodEngineReplaceNodeWithPlaceholder),
		string(MethodEngineAddLink),
		string(MethodEngineRemoveNode),
		string(MethodEngineRemoveNodesByClass),
		string(MethodEngineRemoveLink),
		string(MethodEngineSetNodePosition),
		string(MethodEngineSetNodeName),
		string(MethodEngineRemoveNodePopup),
		string(MethodEngineRemoveNodePopups),
		string(MethodEngineRemoveNodePopupsByType),
		string(MethodEngineCopy),
		string(MethodEnginePaste),
		string(MethodEngineSendInbox),
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
		MethodEngineRemoveNodesByClass,
		MethodEngineRemoveLink,
		MethodEngineSetNodePosition,
		MethodEngineSetNodeName,
		MethodEngineRemoveNodePopup,
		MethodEngineRemoveNodePopups,
		MethodEngineRemoveNodePopupsByType,
		MethodEnginePaste,
		MethodEngineSendInbox,
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
	SubscriptionID uint64                 `json:"subscription_id"`
	Notification   lib.EngineNotification `json:"notification"`
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
	Snapshot pasta.WorkspaceSnapshot `json:"snapshot"`
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
