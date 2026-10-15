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

func TestEnterIsolatedRuntimeStorageLinuxAcceptsBoundUserPath(t *testing.T) {
	t.Setenv("ALMAGEST_LINUX_PROFILE", "linux-user-v1")
	const storage = "/srv/almagest/runtime"
	if err := enterIsolatedRuntimeStorage(storage, storage); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []string{"/srv/almagest/other", "relative"} {
		if err := enterIsolatedRuntimeStorage(storage, requested); err == nil {
			t.Fatalf("accepted request %q", requested)
		}
	}
}
