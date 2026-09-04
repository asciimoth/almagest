package nodekit

import (
	"testing"

	"github.com/asciimoth/formular"
)

func TestConfigSecretNameFieldUsesFrontendSelector(t *testing.T) {
	item := ConfigSecretNameField("secret", "Secret", "selected", true, "invalid")
	if item.Field == nil {
		t.Fatal("secret selector item has no field")
	}
	if item.Kind != formular.FieldText || item.Value != "selected" || item.Selector != SecretNameSelector {
		t.Fatalf("secret selector item = %#v", item)
	}
	if !item.Validation || item.Status != formular.StatusError || item.StatusText != "invalid" {
		t.Fatalf("secret selector status = %#v", item.Field)
	}
}
