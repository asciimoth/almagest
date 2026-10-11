//go:build linux && amd64

package main

import "testing"

func TestEnterIsolatedRuntimeStorageLinuxRequiresFixedRoot(t *testing.T) {
	if err := enterIsolatedRuntimeStorage("/state", "/state"); err != nil {
		t.Fatal(err)
	}
	for _, input := range [][2]string{{"/other", "/state"}, {"/state", "/other"}, {"state", "state"}} {
		if err := enterIsolatedRuntimeStorage(input[0], input[1]); err == nil {
			t.Fatalf("accepted storage %q, request %q", input[0], input[1])
		}
	}
}
