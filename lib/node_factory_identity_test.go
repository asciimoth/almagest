package almagest

import (
	"reflect"
	"testing"

	"github.com/asciimoth/pasta/pasta"
)

// Node records use implementation equality to reject accidental reuse. A
// pointer to a zero-size value does not provide a reliable identity in Go:
// pointers to distinct zero-size values are permitted to compare equal.
func TestPackageNodeClassFactoriesReturnDistinctNonZeroSizePointers(t *testing.T) {
	// Use a non-nil, empty runtime because packageNodeClasses accepts a concrete
	// *engine and passes it through runtime interfaces.
	for _, class := range packageNodeClasses(&engine{}, true) {
		factory, ok := class.(pasta.NodeClassFactory)
		if !ok {
			t.Fatalf("registered class %q has no node factory", class.ClassName())
		}
		t.Run(class.ClassName(), func(t *testing.T) {
			seen := make(map[uintptr]struct{}, 8)
			for i := 0; i < 8; i++ {
				node, err := factory.NewNode(nil)
				if err != nil {
					t.Fatalf("NewNode call %d: %v", i+1, err)
				}
				if node == nil {
					t.Fatalf("NewNode call %d returned nil", i+1)
				}

				typ := reflect.TypeOf(node)
				if typ.Kind() != reflect.Pointer {
					t.Fatalf("NewNode call %d returned %v, want a pointer with per-instance identity", i+1, typ)
				}
				if typ.Elem().Size() == 0 {
					t.Fatalf("NewNode call %d returned pointer to zero-size %v", i+1, typ.Elem())
				}

				identity := reflect.ValueOf(node).Pointer()
				if _, duplicate := seen[identity]; duplicate {
					t.Fatalf("NewNode call %d reused pointer identity %#x", i+1, identity)
				}
				seen[identity] = struct{}{}
			}
		})
	}
}
