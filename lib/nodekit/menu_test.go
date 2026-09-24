package nodekit

import (
	"errors"
	"net/netip"
	"reflect"
	"testing"

	"github.com/asciimoth/formular"

	"github.com/asciimoth/almagest/lib/logging"
)

func TestListTextHelpers(t *testing.T) {
	input := " alpha, beta;gamma\tdelta\r\nepsilon "
	want := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	if got := SplitListText(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitListText() = %#v, want %#v", got, want)
	}
	if got := NormalizeListText(input); got != "alpha\nbeta\ngamma\ndelta\nepsilon" {
		t.Fatalf("NormalizeListText() = %q", got)
	}
}

func TestParseAddrList(t *testing.T) {
	want := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")}
	got, err := ParseAddrList("192.0.2.1, 2001:db8::1/64", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAddrList() = %v, want %v", got, want)
	}
	if _, err := ParseAddrList("192.0.2.0/24", false); err == nil {
		t.Fatal("ParseAddrList() accepted CIDR when allowCIDR is false")
	}
	if _, err := ParseAddrList("not-an-address", true); err == nil {
		t.Fatal("ParseAddrList() accepted an invalid address")
	}
}

func TestParsePort(t *testing.T) {
	for _, test := range []struct {
		name     string
		value    string
		fallback int
		want     int
	}{
		{name: "empty", value: " ", fallback: 8080, want: 8080},
		{name: "numeric", value: " 443 ", fallback: 1, want: 443},
		{name: "service", value: "https", fallback: 1, want: 443},
		{name: "invalid", value: "not-a-service", fallback: 8080, want: 8080},
		{name: "zero", value: "0", fallback: 8080, want: 0},
		{name: "too large", value: "65536", fallback: 8080, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ParsePort(test.value, test.fallback); got != test.want {
				t.Fatalf("ParsePort(%q, %d) = %d, want %d", test.value, test.fallback, got, test.want)
			}
		})
	}
}

func TestMenuFieldHelpers(t *testing.T) {
	text := ConfigTextField("text", "Text", "value", true, "invalid", true)
	if text.Field == nil || !text.Validation || !text.Multiline || text.Status != formular.StatusError || text.StatusText != "invalid" {
		t.Fatalf("ConfigTextField() = %#v", text)
	}
	integer := ConfigIntField("int", "Integer", 12, true, "invalid")
	if integer.Field == nil || integer.Value != 12 || !integer.Validation || integer.Status != formular.StatusError {
		t.Fatalf("ConfigIntField() = %#v", integer)
	}
	floating := ConfigFloatField("float", "Float", 1.5, false, "")
	if floating.Field == nil || floating.Value != 1.5 || !floating.Validation || floating.Status != "" {
		t.Fatalf("ConfigFloatField() = %#v", floating)
	}
	if checkbox := ConfigCheckbox("bool", "Boolean", true); checkbox.Value != true {
		t.Fatalf("ConfigCheckbox() = %#v", checkbox)
	}

	for name, item := range map[string]formular.Item{
		"text":      ReadonlyTextField("text", "Text", "value"),
		"multiline": ReadonlyMultilineTextField("multi", "Multi", "a\nb"),
		"list":      ReadonlyStringListField("list", "List", []string{"a", "b"}),
		"integer":   ReadonlyIntField("int", "Integer", 3),
		"boolean":   ReadonlyBoolField("bool", "Boolean", true),
	} {
		if item.Field == nil || !item.Readonly {
			t.Errorf("%s read-only field = %#v", name, item)
		}
	}
}

func TestLogHelpers(t *testing.T) {
	logf := logging.NewDebugLogFactory(4)
	logger := logf.EngineLogger()
	LogPayload(nil, "read", []byte("ignored"), nil)
	LogResult(nil, "close", nil)
	LogPayload(logger, "read", []byte("data"), nil)
	LogPayload(logger, "write", []byte("data"), errors.New("failed"))
	LogResult(logger, "close", nil)
	LogResult(logger, "flush", errors.New("failed"))

	entries := logf.Drain()
	if len(entries) != 4 {
		t.Fatalf("log entry count = %d, want 4", len(entries))
	}
	want := []string{
		`read bytes=4 payload="data"`,
		`write bytes=4 payload="data" error=failed`,
		"close",
		"flush error=failed",
	}
	for i, entry := range entries {
		if entry.Message != want[i] {
			t.Errorf("entry %d = %q, want %q", i, entry.Message, want[i])
		}
	}
}

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
