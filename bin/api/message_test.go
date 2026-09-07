package api

import (
	"bytes"
	"encoding/json"
	"slices"
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

func TestProjectVersionStatusEventRoundTrip(t *testing.T) {
	msg, err := NewEvent(SurfaceAdmin, string(EventAdminProjectVersionStatus), ProjectVersionStatus{
		Version:      "pre-alpha",
		Highlight:    ProjectVersionHighlightCritical,
		HoverMessage: "Current project state is extremely unstable.",
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
	if got.Kind != MessageEvent || got.Surface != SurfaceAdmin || got.Method != string(EventAdminProjectVersionStatus) {
		t.Fatalf("unexpected message: %#v", got)
	}

	status, err := DecodePayload[ProjectVersionStatus](got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if status.Highlight != ProjectVersionHighlightCritical || status.Version != "pre-alpha" {
		t.Fatalf("unexpected status payload: %#v", status)
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

func TestBatchNodePayloadsRoundTrip(t *testing.T) {
	removeMsg, err := NewRequest("1", SurfaceEngine, string(MethodEngineRemoveNodes), IDsRequest{
		Workspace: "main",
		IDs:       []uint64{7, 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	removeReq, err := DecodePayload[IDsRequest](removeMsg.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if removeReq.Workspace != "main" || len(removeReq.IDs) != 2 || removeReq.IDs[1] != 8 {
		t.Fatalf("unexpected remove_nodes payload: %#v", removeReq)
	}

	moveMsg, err := NewRequest("2", SurfaceEngine, string(MethodEngineMoveNodes), MoveNodesRequest{
		Workspace: "main",
		Positions: map[uint64]string{
			7: "10 20",
			8: "30 40",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	moveReq, err := DecodePayload[MoveNodesRequest](moveMsg.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if moveReq.Workspace != "main" || moveReq.Positions[7] != "10 20" || moveReq.Positions[8] != "30 40" {
		t.Fatalf("unexpected move_nodes payload: %#v", moveReq)
	}
}

func TestRequiresMutationLease(t *testing.T) {
	if !RequiresMutationLease(SurfaceEngine, string(MethodEngineAddNode)) {
		t.Fatal("add_node should require mutation lease")
	}
	if !RequiresMutationLease(SurfaceEngine, string(MethodEngineRemoveNodes)) {
		t.Fatal("remove_nodes should require mutation lease")
	}
	if !RequiresMutationLease(SurfaceEngine, string(MethodEngineMoveNodes)) {
		t.Fatal("move_nodes should require mutation lease")
	}
	if !RequiresMutationLease(SurfaceEngine, string(MethodEngineTriggerNode)) {
		t.Fatal("trigger_node should require mutation lease")
	}
	if RequiresMutationLease(SurfaceEngine, string(MethodEngineWorkspaceSnapshot)) {
		t.Fatal("workspace_snapshot should not require mutation lease")
	}
	if RequiresMutationLease(SurfaceEngine, string(MethodEngineWorkersSnapshot)) {
		t.Fatal("workers_snapshot should not require mutation lease")
	}
	if RequiresMutationLease(SurfaceAdmin, string(MethodAdminPing)) {
		t.Fatal("admin ping should not require mutation lease")
	}
	if !RequiresMutationLease(SurfaceSecrets, string(MethodSecretsSetValue)) {
		t.Fatal("secrets set_value should require mutation lease")
	}
	if RequiresMutationLease(SurfaceSecrets, string(MethodSecretsSnapshot)) {
		t.Fatal("secrets snapshot should not require mutation lease")
	}
	if !RequiresMutationLease(SurfaceTLS, string(MethodTLSSetCert)) {
		t.Fatal("TLS set_cert should require mutation lease")
	}
	if !RequiresMutationLease(SurfaceTLS, string(MethodTLSSetBuiltinCA)) {
		t.Fatal("TLS set_builtin_ca_options should require mutation lease")
	}
	if !RequiresMutationLease(SurfaceTLS, string(MethodTLSReloadOSCerts)) {
		t.Fatal("TLS reload_os_certs should require mutation lease")
	}
	if RequiresMutationLease(SurfaceTLS, string(MethodTLSSnapshot)) {
		t.Fatal("TLS snapshot should not require mutation lease")
	}
}

func TestEngineMethodsIncludesWorkersSnapshot(t *testing.T) {
	required := map[string]bool{
		string(MethodEngineWorkersSnapshot): false,
		string(MethodEngineRemoveNodes):     false,
		string(MethodEngineMoveNodes):       false,
		string(MethodEngineTriggerNode):     false,
	}
	for _, method := range EngineMethods() {
		if _, ok := required[method]; ok {
			required[method] = true
		}
	}
	for method, found := range required {
		if !found {
			t.Fatalf("EngineMethods does not include %s", method)
		}
	}
}

func TestDNSMethodsAndSurfaceAreAdvertised(t *testing.T) {
	if !slices.Contains(Surfaces(), string(SurfaceDNS)) {
		t.Fatal("Surfaces does not include dns")
	}
	required := map[string]bool{
		string(MethodDNSSubscribeCaches): false,
		string(MethodDNSSubscribeKeys):   false,
		string(MethodDNSSubscribeValue):  false,
		string(MethodDNSUnsubscribe):     false,
		string(MethodDNSSet):             false,
		string(MethodDNSDelete):          false,
		string(MethodDNSDrop):            false,
	}
	for _, method := range DNSMethods() {
		if _, ok := required[method]; ok {
			required[method] = true
		}
	}
	for method, found := range required {
		if !found {
			t.Fatalf("DNSMethods does not include %s", method)
		}
	}
}

func TestConfigMethodsAndSurfaceAreAdvertised(t *testing.T) {
	if !slices.Contains(Surfaces(), string(SurfaceConfig)) {
		t.Fatal("Surfaces does not include config")
	}
	methods := ConfigMethods()
	for _, method := range []ConfigMethod{MethodConfigSnapshot, MethodConfigUpdate} {
		if !slices.Contains(methods, string(method)) {
			t.Fatalf("ConfigMethods does not include %q", method)
		}
	}
	if IsConfigMutation(string(MethodConfigSnapshot)) {
		t.Fatal("config snapshot is a mutation")
	}
	if !RequiresMutationLease(SurfaceConfig, string(MethodConfigUpdate)) {
		t.Fatal("config update does not require a mutation lease")
	}
	if SchedulesConfigAutosave(SurfaceConfig, string(MethodConfigUpdate)) {
		t.Fatal("config update schedules a redundant autosave")
	}
}

func TestSecretsMethodsAndSurfaceAreAdvertised(t *testing.T) {
	if !slices.Contains(Surfaces(), string(SurfaceSecrets)) {
		t.Fatal("Surfaces does not include secrets")
	}
	required := map[string]bool{
		string(MethodSecretsSnapshot):    false,
		string(MethodSecretsSubscribe):   false,
		string(MethodSecretsUnsubscribe): false,
		string(MethodSecretsSetValue):    false,
		string(MethodSecretsSetNodes):    false,
		string(MethodSecretsRename):      false,
		string(MethodSecretsDelete):      false,
	}
	for _, method := range SecretsMethods() {
		if _, ok := required[method]; ok {
			required[method] = true
		}
	}
	for method, found := range required {
		if !found {
			t.Fatalf("SecretsMethods does not include %s", method)
		}
	}
}

func TestTLSMethodsAndSurfaceAreAdvertised(t *testing.T) {
	if !slices.Contains(Surfaces(), string(SurfaceTLS)) {
		t.Fatal("Surfaces does not include tls")
	}
	required := map[string]bool{
		string(MethodTLSSnapshot):      false,
		string(MethodTLSSubscribe):     false,
		string(MethodTLSUnsubscribe):   false,
		string(MethodTLSBuiltinCA):     false,
		string(MethodTLSSetBuiltinCA):  false,
		string(MethodTLSReloadOSCerts): false,
		string(MethodTLSSetCert):       false,
		string(MethodTLSRenameCert):    false,
		string(MethodTLSDeleteCert):    false,
		string(MethodTLSSetPolicy):     false,
		string(MethodTLSRenamePolicy):  false,
		string(MethodTLSDeletePolicy):  false,
	}
	for _, method := range TLSMethods() {
		if _, ok := required[method]; ok {
			required[method] = true
		}
	}
	for method, found := range required {
		if !found {
			t.Fatalf("TLSMethods does not include %s", method)
		}
	}
}
