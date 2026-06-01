package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	msg, err := NewRequest("1", SurfaceEngine, string(MethodEngineAddNode), AddNodeRequest{
		Workspace: "main",
		Class:     "almagest/MockNetwork",
		Name:      "net",
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := MarshalMessage(msg)
	if err != nil {
		t.Fatal(err)
	}

	got, err := UnmarshalMessage(data)
	if err != nil {
		t.Fatal(err)
	}

	req, err := DecodePayload[AddNodeRequest](got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if req.Workspace != "main" || req.Name != "net" {
		t.Fatalf("unexpected request payload: %#v", req)
	}
}

func TestCodecRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(&buf, &buf)

	msg, err := NewEvent(SurfaceEngine, string(EventEngineNotification), EngineNotificationEvent{
		SubscriptionID: 42,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := codec.Write(msg); err != nil {
		t.Fatal(err)
	}

	got, err := codec.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != MessageEvent || got.Surface != SurfaceEngine {
		t.Fatalf("unexpected message: %#v", got)
	}
}

func TestValidateResponseNeedsResultOrError(t *testing.T) {
	msg := Message{Version: Version, Kind: MessageResponse, ID: "1"}
	if err := msg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRawPayloadIsPreserved(t *testing.T) {
	raw := json.RawMessage(`{"x":1}`)
	msg, err := NewRequest("1", SurfaceEngine, string(MethodEngineSendInbox), SendInboxRequest{
		Workspace: "main",
		Node:      7,
		Payload:   raw,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, err := DecodePayload[SendInboxRequest](msg.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(req.Payload, raw) {
		t.Fatalf("raw payload mismatch: %s", req.Payload)
	}
}

func TestRequiresMutationLease(t *testing.T) {
	if !RequiresMutationLease(SurfaceEngine, string(MethodEngineAddNode)) {
		t.Fatal("add_node should require mutation lease")
	}
	if RequiresMutationLease(SurfaceEngine, string(MethodEngineWorkspaceSnapshot)) {
		t.Fatal("workspace_snapshot should not require mutation lease")
	}
	if RequiresMutationLease(SurfaceAdmin, string(MethodAdminPing)) {
		t.Fatal("admin ping should not require mutation lease")
	}
}
