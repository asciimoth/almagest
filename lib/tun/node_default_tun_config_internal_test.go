package tunnodes

import (
	"reflect"
	"testing"

	"github.com/asciimoth/configer/configer"
	"github.com/asciimoth/formular"
	"github.com/asciimoth/gonnect/sysnet"
)

func TestDefaultTunConfigConversionVariants(t *testing.T) {
	elements := []formular.ArrayElementValue{
		{Values: map[string]any{"value": " first "}},
		{Values: map[string]any{"value": ""}},
		{Values: map[string]any{"value": "second"}},
	}
	if got, ok := defaultTunStringListFromAny(elements); !ok || got != "first\nsecond" {
		t.Fatalf("element string list = %q, %t", got, ok)
	}
	if got, ok := defaultTunStringListFromAny([]any{
		map[string]any{"id": "one", "template": "entry", "values": map[string]any{"value": " third "}},
		42,
	}); !ok || got != "third" {
		t.Fatalf("generic string list = %q, %t", got, ok)
	}
	if got, ok := defaultTunStringListFromAny("text"); !ok || got != "text" {
		t.Fatalf("scalar string list = %q, %t", got, ok)
	}
	if _, ok := defaultTunStringListFromAny(42); ok {
		t.Fatal("numeric string list was accepted")
	}

	ruleElements := []formular.ArrayElementValue{
		{Values: map[string]any{"type": " domain ", "rule": " example.test "}},
		{Values: map[string]any{}},
	}
	wantRules := []sysnet.Rule{{Type: "domain", Rule: "example.test"}}
	if got, ok := defaultTunRulesFromAny(ruleElements); !ok || !reflect.DeepEqual(got, wantRules) {
		t.Fatalf("element rules = %#v, %t", got, ok)
	}
	if got, ok := defaultTunRulesFromAny([]any{
		map[string]any{"id": "one", "template": "entry", "values": map[string]any{"type": "domain", "rule": "example.test"}},
		false,
	}); !ok || !reflect.DeepEqual(got, wantRules) {
		t.Fatalf("generic rules = %#v, %t", got, ok)
	}
	if _, ok := defaultTunRulesFromAny("invalid"); ok {
		t.Fatal("scalar rules were accepted")
	}
}

func TestDefaultTunStoredRuleVariants(t *testing.T) {
	want := []sysnet.Rule{{Type: "domain", Rule: "example.test"}}
	tests := []struct {
		name string
		raw  any
	}{
		{name: "rules", raw: []sysnet.Rule{{Type: " domain ", Rule: " example.test "}, {}}},
		{name: "string maps", raw: []map[string]string{{"type": " domain ", "rule": " example.test "}, {}}},
		{name: "any maps", raw: []map[string]any{{"type": " domain ", "rule": " example.test "}, {}}},
		{name: "mixed", raw: []any{
			sysnet.Rule{Type: " domain ", Rule: " example.test "},
			map[string]string{}, map[string]any{}, "ignored",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := defaultTunRulesFromConfigValue(test.raw)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rules = %#v, want %#v", got, want)
			}
		})
	}
	if got := defaultTunRulesFromConfigValue("invalid"); got != nil {
		t.Fatalf("invalid rules = %#v", got)
	}
	if got := readDefaultTunRulesConfig(nil, "rules"); got != nil {
		t.Fatalf("nil config rules = %#v", got)
	}
	if got := readDefaultTunRulesConfig(configer.NewMemory(nil), "missing"); got != nil {
		t.Fatalf("missing config rules = %#v", got)
	}
	if got := defaultTunRulesConfig(want); !reflect.DeepEqual(got, []map[string]string{{"type": "domain", "rule": "example.test"}}) {
		t.Fatalf("encoded rules = %#v", got)
	}
}

func TestDefaultTunMergeHelpers(t *testing.T) {
	if got := mergeDefaultTunStringLists([]string{" a ", "", "b"}, []string{"b", "c"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("merged list = %#v", got)
	}
	for _, test := range []struct{ a, b, want int }{{0, 10, 10}, {10, 0, 10}, {10, 20, 10}} {
		if got := mergeDefaultTunMTU(test.a, test.b); got != test.want {
			t.Fatalf("mergeDefaultTunMTU(%d, %d) = %d", test.a, test.b, got)
		}
	}
}
