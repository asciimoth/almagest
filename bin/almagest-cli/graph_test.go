package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/asciimoth/almagest/bin/api"
	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func TestGraphMapPrintsCompactConfigLikeNodes(t *testing.T) {
	resp, err := api.NewResponse("1", api.WorkspaceSnapshotResponse{Snapshot: pasta.WorkspaceSnapshot{
		Nodes: map[uint64]pasta.NodeSnapshot{
			10: {Name: "source", Class: "test/source"},
			20: {Name: "target", Class: "test/target"},
		},
		Ports: map[uint64]pasta.PortSnapshot{
			100: {Node: 10, Direction: "right", Name: "out"},
			200: {Node: 20, Direction: "left", Name: "in"},
		},
		Links: map[uint64]pasta.LinkSnapshot{
			1: {Type: "any", RightPort: 100, RightPortNode: 10, LeftPort: 200, LeftPortNode: 20},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransport{reads: []api.Message{resp}}
	var stdout, stderr bytes.Buffer

	err = run(context.Background(), []string{"graph", "map"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	out := stdout.String()
	for _, want := range []string{`"source"`, `"ID": 10`, `"Class": "test/source"`, `"out -> [target] in"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout = %s, want substring %s", out, want)
		}
	}
}

func TestGraphApplyMenuSendsFormApply(t *testing.T) {
	menu := formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(12), MenuGeneration: 7},
		Blocks: []formular.Block{{
			ID:         "config",
			Generation: 3,
			Form:       true,
			Items: []formular.Item{{
				Type:  formular.ItemField,
				ID:    "host",
				Field: &formular.Field{Kind: formular.FieldText, Value: "old"},
			}},
		}},
	}
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.SubscribeNotificationsResponse{SubscriptionID: 55}),
		mustResponse(t, "2", struct{}{}),
		mustMenuEvent(t, 12, menu),
		mustResponse(t, "3", struct{}{}),
		mustResponse(t, "4", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"graph", "apply-menu", "--node", "12", "--json", `{"config":{"host":"new"}}`}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(ft.writes) != 4 {
		t.Fatalf("writes = %d, want 4", len(ft.writes))
	}
	var req api.SendNodeFormularMessageRequest
	if err := json.Unmarshal(ft.writes[3].Payload, &req); err != nil {
		t.Fatal(err)
	}
	var msg formular.FormApplyMessage
	if err := json.Unmarshal(req.Message, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != formular.MessageFormApply || msg.MenuID != pasta.NodeMenuID(12) || msg.BlockID != "config" || msg.Values["host"] != "new" {
		t.Fatalf("formular message = %#v", msg)
	}
}

func TestGraphMenuPrintsSnapshotSentBeforeSubscribeResponse(t *testing.T) {
	menu := formular.MenuSnapshotMessage{
		MessageBase: formular.MessageBase{Type: formular.MessageMenuSnapshot, MenuID: pasta.NodeMenuID(12), MenuGeneration: 7},
		Blocks: []formular.Block{{
			ID:         "config",
			Generation: 3,
			Form:       true,
		}},
	}
	ft := &fakeTransport{reads: []api.Message{
		mustResponse(t, "1", api.SubscribeNotificationsResponse{SubscriptionID: 55}),
		mustMenuEvent(t, 12, menu),
		mustResponse(t, "2", struct{}{}),
		mustResponse(t, "3", struct{}{}),
	}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"graph", "menu", "--node", "12"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"menuId": "NODE12MENU"`) || !strings.Contains(stdout.String(), `"id": "config"`) {
		t.Fatalf("stdout = %s, want node menu snapshot", stdout.String())
	}
}

func TestGraphClickMenuButtonSendsButtonPress(t *testing.T) {
	ft := &fakeTransport{reads: []api.Message{mustResponse(t, "1", struct{}{})}}
	var stdout, stderr bytes.Buffer

	err := run(context.Background(), []string{"graph", "click-menu-button", "--node", "12", "--block", "request", "--button", "send"}, nil, &stdout, &stderr, func(context.Context, endpoint) (apiTransport, error) {
		return ft, nil
	})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	var req api.SendNodeFormularMessageRequest
	if err := json.Unmarshal(ft.writes[0].Payload, &req); err != nil {
		t.Fatal(err)
	}
	var msg formular.ButtonPressMessage
	if err := json.Unmarshal(req.Message, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != formular.MessageButtonPress || msg.MenuID != pasta.NodeMenuID(12) || msg.BlockID != "request" || msg.ButtonID != "send" {
		t.Fatalf("button message = %#v", msg)
	}
}

func mustResponse(t *testing.T, id string, result any) api.Message {
	t.Helper()
	msg, err := api.NewResponse(id, result)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func mustMenuEvent(t *testing.T, node uint64, menu formular.MenuSnapshotMessage) api.Message {
	t.Helper()
	msg, err := api.NewEvent(api.SurfaceEngine, string(api.EventEngineNotification), api.EngineNotificationEvent{
		SubscriptionID: 55,
		Notification: almagest.EngineNotification{
			Kind:        almagest.NotificationWorkspace,
			WorkspaceID: "main",
			Workspace: &pasta.WorkspaceNotification{
				Kind:     pasta.NotificationNodeMenu,
				ID:       node,
				Formular: menu,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
