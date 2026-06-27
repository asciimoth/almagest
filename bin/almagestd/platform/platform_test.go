package platform

import (
	"bytes"
	"strings"
	"testing"
)

func TestSplitEmbeddedProtectedConfig(t *testing.T) {
	configData := []byte(`{
  "Config": { "Save": { "IdleMilliseconds": 1250 } },
  "Protected": {
    "Admin": { "Web": 8080 },
    "System": { "Type": "debug" }
  },
  "nodes": {}
}`)
	defaultProtectedData := []byte(`{"Admin":{"Web":8090}}`)

	mainData, protectedData := splitEmbeddedProtectedConfig(
		configData,
		defaultProtectedData,
	)

	mainText := string(mainData)
	if strings.Contains(mainText, `"Protected"`) {
		t.Fatalf("main config still contains Protected:\n%s", mainText)
	}
	if !strings.Contains(mainText, `"IdleMilliseconds": 1250`) {
		t.Fatalf("main config lost Config.Save:\n%s", mainText)
	}

	protectedText := string(protectedData)
	if strings.Contains(protectedText, `"Web":8090`) {
		t.Fatalf("protected config used default instead of embedded:\n%s", protectedText)
	}
	if !strings.Contains(protectedText, `"Web": 8080`) {
		t.Fatalf("protected config lost embedded Admin.Web:\n%s", protectedText)
	}
	if !strings.Contains(protectedText, `"Type": "debug"`) {
		t.Fatalf("protected config lost embedded System.Type:\n%s", protectedText)
	}
}

func TestSplitEmbeddedProtectedConfigUsesDefaultWhenMissing(t *testing.T) {
	configData := []byte(`{"Config":{}}`)
	defaultProtectedData := []byte(`{"Admin":{"Web":8090}}`)

	mainData, protectedData := splitEmbeddedProtectedConfig(
		configData,
		defaultProtectedData,
	)

	if !bytes.Equal(mainData, configData) {
		t.Fatalf("main config changed:\n%s", string(mainData))
	}
	if !bytes.Equal(protectedData, defaultProtectedData) {
		t.Fatalf("protected config = %s, want default %s", protectedData, defaultProtectedData)
	}
}
