package links

import (
	"reflect"
	"testing"
)

func TestSameComparableValue(t *testing.T) {
	if !SameComparableValue("a", "a") {
		t.Fatal("equal strings did not compare equal")
	}
	if SameComparableValue("a", "b") {
		t.Fatal("different strings compared equal")
	}
	if SameComparableValue("1", 1) {
		t.Fatal("different types compared equal")
	}
	if SameComparableValue([]string{"a"}, []string{"a"}) {
		t.Fatal("non-comparable values compared equal")
	}
	if !SameComparableValue(nil, nil) {
		t.Fatal("nil values did not compare equal")
	}
}

func TestTunConfigByLinkSetMergeDelete(t *testing.T) {
	store := TunConfigByLink{}
	cfg := TunConfig{
		Addrs:  []string{"10.0.0.2/32", "fd00::2/128"},
		Routes: []string{"10.20.0.0/24", "fd00:20::/64"},
		MTU:    1420,
	}
	store.Set(2, cfg)
	cfg.Addrs[0] = "mutated"
	cfg.Routes[0] = "mutated"
	store.Set(1, TunConfig{
		Addrs:  []string{"10.0.0.3/32", "10.0.0.2/32"},
		Routes: []string{"10.10.0.0/24", "10.20.0.0/24"},
		MTU:    1280,
	})
	store.Set(0, TunConfig{Addrs: []string{"ignored"}, Routes: []string{"ignored"}, MTU: 1})

	merged := store.Merge()
	want := TunConfig{
		Addrs:  []string{"10.0.0.3/32", "10.0.0.2/32", "fd00::2/128"},
		Routes: []string{"10.10.0.0/24", "10.20.0.0/24", "fd00:20::/64"},
		MTU:    1280,
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("Merge() = %#v, want %#v", merged, want)
	}

	store.Delete(1)
	merged = store.Merge()
	want = TunConfig{
		Addrs:  []string{"10.0.0.2/32", "fd00::2/128"},
		Routes: []string{"10.20.0.0/24", "fd00:20::/64"},
		MTU:    1420,
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("Merge() after Delete = %#v, want %#v", merged, want)
	}
}
