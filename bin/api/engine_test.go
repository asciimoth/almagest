package api

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	almagest "github.com/asciimoth/almagest/lib"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/pasta/pasta"
)

func TestSortedNodeClassSnapshotsCanonicalOrder(t *testing.T) {
	classes := map[string]pasta.NodeClassSnapshot{
		"beta/Beta":       {Class: "beta/Beta", PrimaryType: "beta/value"},
		"almagest/NoType": {Class: "almagest/NoType"},
		"other/Untyped":   {Class: "other/Untyped"},
		"other/Beta":      {Class: "other/Beta", PrimaryType: "other/b"},
		"almagest/Zed":    {Class: "almagest/Zed", PrimaryType: "almagest/z"},
		"pasta/PopUp":     {Class: "pasta/PopUp", PrimaryType: "pasta/popup"},
		"alpha/Beta":      {Class: "alpha/Beta", PrimaryType: "alpha/value"},
		"almagest/Alpha":  {Class: "almagest/Alpha", PrimaryType: "almagest/a"},
		"other/Alpha":     {Class: "other/Alpha", PrimaryType: "other/a"},
		"alpha/Alpha":     {Class: "alpha/Alpha", PrimaryType: "alpha/value"},
	}

	got := classNames(SortedNodeClassSnapshots(classes))
	want := []string{
		"almagest/Alpha",
		"almagest/Zed",
		"almagest/NoType",
		"pasta/PopUp",
		"alpha/Alpha",
		"alpha/Beta",
		"beta/Beta",
		"other/Alpha",
		"other/Beta",
		"other/Untyped",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted classes = %v, want %v", got, want)
	}
}

func TestWorkspaceSnapshotClassesAreJSONList(t *testing.T) {
	snapshot := NewWorkspaceSnapshot(pasta.WorkspaceSnapshot{
		Classes: map[string]pasta.NodeClassSnapshot{
			"pasta/PopUp":    {Class: "pasta/PopUp", PrimaryType: "pasta/popup"},
			"almagest/Tun":   {Class: "almagest/Tun", PrimaryType: "almagest/tun"},
			"example/NoType": {Class: "example/NoType"},
		},
	})
	data, err := json.Marshal(WorkspaceSnapshotResponse{Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}

	var raw struct {
		Snapshot struct {
			Classes json.RawMessage `json:"classes"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if trimmed := bytes.TrimSpace(raw.Snapshot.Classes); len(trimmed) == 0 || trimmed[0] != '[' {
		t.Fatalf("classes JSON = %s, want array", raw.Snapshot.Classes)
	}

	var decoded WorkspaceSnapshotResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	got := classNames(decoded.Snapshot.Classes)
	want := []string{"almagest/Tun", "pasta/PopUp", "example/NoType"}
	if !slices.Equal(got, want) {
		t.Fatalf("decoded classes = %v, want %v", got, want)
	}
}

func TestEngineNotificationConvertsWorkspaceSnapshotClasses(t *testing.T) {
	notification := almagest.EngineNotification{
		Kind:        almagest.NotificationWorkspace,
		WorkspaceID: "main",
		Workspace: &pasta.WorkspaceNotification{
			Kind: pasta.NotificationWorkspaceSnapshot,
			Snapshot: &pasta.WorkspaceSnapshot{
				Classes: map[string]pasta.NodeClassSnapshot{
					"pasta/PopUp":  {Class: "pasta/PopUp"},
					"almagest/Tun": {Class: "almagest/Tun", PrimaryType: "almagest/tun"},
				},
			},
		},
	}

	got := NewEngineNotification(notification)
	if got.Workspace == nil || got.Workspace.Snapshot == nil {
		t.Fatalf("converted notification = %#v, want workspace snapshot", got)
	}
	if got := classNames(got.Workspace.Snapshot.Classes); !slices.Equal(got, []string{"almagest/Tun", "pasta/PopUp"}) {
		t.Fatalf("notification snapshot classes = %v, want sorted list", got)
	}
}

func TestEngineNotificationPreservesCaptchaDialogOnWire(t *testing.T) {
	dialog := formular.CaptchaDialog(
		"amnesia-captcha-1-1",
		"Complete captcha",
		"Verify this request.",
		formular.DialogResourceFromBytes("challenge", "image/png", "Captcha challenge", []byte("image")),
	)
	notification := NewEngineNotification(almagest.EngineNotification{
		Kind:        almagest.NotificationWorkspace,
		WorkspaceID: "main",
		Workspace: &pasta.WorkspaceNotification{
			Kind:     pasta.NotificationNodeMenu,
			ID:       1,
			Formular: formular.DialogCreate(pasta.NodeMenuID(1), 1, dialog),
		},
	})

	data, err := json.Marshal(notification)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Workspace struct {
			Formular struct {
				Type   string `json:"type"`
				MenuID string `json:"menuId"`
				Dialog struct {
					Kind      string `json:"kind"`
					Resources []struct {
						MIMEType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"resources"`
				} `json:"dialog"`
			} `json:"formular"`
		} `json:"workspace_notification"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	message := wire.Workspace.Formular
	if message.Type != formular.MessageDialogCreate || message.MenuID != pasta.NodeMenuID(1) ||
		message.Dialog.Kind != formular.DialogKindCaptcha || len(message.Dialog.Resources) != 1 ||
		message.Dialog.Resources[0].MIMEType != "image/png" || message.Dialog.Resources[0].Data == "" {
		t.Fatalf("captcha dialog wire message = %#v", message)
	}
}

func classNames(classes []pasta.NodeClassSnapshot) []string {
	out := make([]string, 0, len(classes))
	for _, class := range classes {
		out = append(out, class.Class)
	}
	return out
}
